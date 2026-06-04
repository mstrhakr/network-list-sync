package syncer

import (
	"bytes"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	npmclient "github.com/mstrhakr/network-list-sync/internal/npm"
	"github.com/mstrhakr/network-list-sync/internal/store"
	"github.com/mstrhakr/network-list-sync/internal/unifi"
)

// SyncResult captures the outcome of a single sync execution.
type SyncResult struct {
	Status      string `json:"status"`
	Message     string `json:"message"`
	ChangesMade int    `json:"changes_made"`
	Details     string `json:"details"`
}

// Syncer executes DNS-to-UniFi firewall group sync operations.
type Syncer struct {
	running sync.Map // prevents concurrent runs of the same job
}

func New() *Syncer {
	return &Syncer{}
}

// Run executes a sync job, logging results to the store.
func (s *Syncer) Run(db *store.Store, jobID int64) SyncResult {
	if _, loaded := s.running.LoadOrStore(jobID, true); loaded {
		return SyncResult{Status: "skipped", Message: "Job is already running"}
	}
	defer s.running.Delete(jobID)

	job, err := db.GetJob(jobID)
	if err != nil {
		return SyncResult{Status: "error", Message: fmt.Sprintf("load job: %v", err)}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	runLog := &store.RunLog{
		JobID:     jobID,
		StartedAt: now,
		Status:    "running",
	}
	logID, err := db.CreateRunLog(runLog)
	if err != nil {
		log.Printf("Job %d: failed to create run log: %v", jobID, err)
		return SyncResult{Status: "error", Message: fmt.Sprintf("create run log: %v", err)}
	}

	result := s.execute(db, job)

	finished := time.Now().UTC().Format(time.RFC3339)
	runLog.ID = logID
	runLog.FinishedAt = &finished
	runLog.Status = result.Status
	runLog.Message = result.Message
	runLog.ChangesMade = result.ChangesMade
	runLog.Details = result.Details
	db.UpdateRunLog(runLog)
	db.UpdateJobLastRun(jobID, finished, result.Status+": "+result.Message)

	log.Printf("Job %d (%s): %s - %s", jobID, job.Name, result.Status, result.Message)
	return result
}

func (s *Syncer) execute(db *store.Store, job *store.SyncJob) SyncResult {
	servers, err := db.ListEnabledDNSServerAddresses()
	if err != nil {
		return SyncResult{Status: "error", Message: fmt.Sprintf("load DNS servers: %v", err)}
	}
	if len(servers) == 0 {
		return SyncResult{Status: "error", Message: "no DNS servers configured: add at least one enabled DNS server"}
	}
	hostIPs, err := ResolveHostnames(job.Hostnames, servers)
	if err != nil {
		return SyncResult{Status: "error", Message: fmt.Sprintf("DNS resolution: %v", err)}
	}

	now := time.Now().UTC()
	retention := observedIPRetention(job)
	if retention > 0 {
		retentionCutoff := now.Add(-retention).Format(time.RFC3339)
		if err := db.UpsertObservedIPs(job.ID, hostIPs, now.Format(time.RFC3339)); err != nil {
			return SyncResult{Status: "error", Message: fmt.Sprintf("record observed IPs: %v", err)}
		}
		if err := db.DeleteExpiredObservedIPs(job.ID, retentionCutoff); err != nil {
			return SyncResult{Status: "error", Message: fmt.Sprintf("cleanup observed IPs: %v", err)}
		}
		observedIPs, err := db.ListObservedIPs(job.ID, retentionCutoff)
		if err != nil {
			return SyncResult{Status: "error", Message: fmt.Sprintf("load observed IPs: %v", err)}
		}
		hostIPs = mergeResolvedIPs(observedIPs, hostIPs)
	} else {
		if err := db.DeleteObservedIPs(job.ID); err != nil {
			return SyncResult{Status: "error", Message: fmt.Sprintf("clear observed IPs: %v", err)}
		}
	}

	targets, err := db.ListJobTargets(job.ID)
	if err != nil {
		return SyncResult{Status: "error", Message: fmt.Sprintf("load job targets: %v", err)}
	}
	if len(targets) == 0 {
		targets = []store.JobTarget{{ControllerID: job.ControllerID, NetworkListID: job.NetworkListID}}
	}

	newIPs := SortedIPs(hostIPs)
	totalChanges := 0
	succeeded := 0
	failed := 0
	var detailParts []string

	for _, target := range targets {
		ctrl, err := db.GetController(target.ControllerID)
		if err != nil {
			failed++
			detailParts = append(detailParts, fmt.Sprintf("[target controller_id=%d list=%s]\nerror: load endpoint: %v", target.ControllerID, target.NetworkListID, err))
			continue
		}

		provider := strings.ToLower(strings.TrimSpace(ctrl.Provider))
		if provider == "" {
			provider = "unifi"
		}

		result, err := s.syncTarget(provider, ctrl, target.NetworkListID, newIPs, hostIPs)
		if err != nil {
			failed++
			detailParts = append(detailParts, fmt.Sprintf("[%s:%s @ %s]\nerror: %v", provider, target.NetworkListID, ctrl.Name, err))
			continue
		}

		succeeded++
		totalChanges += result.changes
		detailParts = append(detailParts, fmt.Sprintf("[%s:%s @ %s]\n%s", provider, target.NetworkListID, ctrl.Name, result.details))
	}

	status := "success"
	message := fmt.Sprintf("Targets: %d succeeded, %d failed, total changes %d", succeeded, failed, totalChanges)
	if failed > 0 {
		status = "error"
	}

	return SyncResult{
		Status:      status,
		Message:     message,
		ChangesMade: totalChanges,
		Details:     strings.Join(detailParts, "\n\n"),
	}
}

type targetSyncResult struct {
	changes int
	details string
}

func (s *Syncer) syncTarget(provider string, ctrl *store.Controller, listID string, newIPs []string, hostIPs map[string]string) (targetSyncResult, error) {
	if provider == "npm" {
		client, err := npmclient.NewClient(ctrl.URL, ctrl.Site, ctrl.APIKey, ctrl.SkipTLSVerify)
		if err != nil {
			return targetSyncResult{}, fmt.Errorf("NPM API client: %w", err)
		}
		nl, err := client.GetNetworkList(listID)
		if err != nil {
			return targetSyncResult{}, fmt.Errorf("get access list: %w", err)
		}
		oldIPs := ExtractNPMIPsFromItems(nl.Items)
		sort.Strings(oldIPs)
		added, removed, kept := DiffIPs(oldIPs, newIPs)
		if len(added) == 0 && len(removed) == 0 {
			return targetSyncResult{details: fmt.Sprintf("No changes needed (%d IPs match)", len(kept))}, nil
		}
		nl.Items = IPsToNPMItems(newIPs)
		if err := client.UpdateNetworkList(nl); err != nil {
			return targetSyncResult{}, fmt.Errorf("update access list: %w", err)
		}
		return targetSyncResult{changes: len(added) + len(removed), details: FormatDiff(added, removed, kept, hostIPs)}, nil
	}

	client, err := unifi.NewClient(ctrl.URL, ctrl.Site, ctrl.APIKey, ctrl.SkipTLSVerify)
	if err != nil {
		return targetSyncResult{}, fmt.Errorf("UniFi API client: %w", err)
	}
	nl, err := client.GetNetworkList(listID)
	if err != nil {
		return targetSyncResult{}, fmt.Errorf("get network list: %w", err)
	}
	oldIPs := ExtractUniFiIPsFromItems(nl.Items)
	sort.Strings(oldIPs)
	added, removed, kept := DiffIPs(oldIPs, newIPs)
	if len(added) == 0 && len(removed) == 0 {
		return targetSyncResult{details: fmt.Sprintf("No changes needed (%d IPs match)", len(kept))}, nil
	}
	nl.Items = IPsToUniFiItems(newIPs)
	if err := client.UpdateNetworkList(nl); err != nil {
		return targetSyncResult{}, fmt.Errorf("update network list: %w", err)
	}
	return targetSyncResult{changes: len(added) + len(removed), details: FormatDiff(added, removed, kept, hostIPs)}, nil
}

// SortedIPs returns IPv4 addresses and IPv4 CIDRs from a host-IP map in a stable order.
func SortedIPs(hostIPs map[string]string) []string {
	ips := make([]string, 0, len(hostIPs))
	for ip := range hostIPs {
		ips = append(ips, ip)
	}
	sort.Slice(ips, func(i, j int) bool {
		leftKey, leftPrefix, leftOK := ipSortKey(ips[i])
		rightKey, rightPrefix, rightOK := ipSortKey(ips[j])
		switch {
		case leftOK && rightOK:
			if compare := bytes.Compare(leftKey, rightKey); compare != 0 {
				return compare < 0
			}
			return leftPrefix < rightPrefix
		case leftOK:
			return true
		case rightOK:
			return false
		default:
			return ips[i] < ips[j]
		}
	})
	return ips
}

// DiffIPs computes added, removed, and kept sets between old and new IP lists.
func DiffIPs(oldIPs, newIPs []string) (added, removed, kept []string) {
	oldSet := make(map[string]bool, len(oldIPs))
	for _, ip := range oldIPs {
		oldSet[ip] = true
	}
	newSet := make(map[string]bool, len(newIPs))
	for _, ip := range newIPs {
		newSet[ip] = true
	}
	for _, ip := range newIPs {
		if oldSet[ip] {
			kept = append(kept, ip)
		} else {
			added = append(added, ip)
		}
	}
	for _, ip := range oldIPs {
		if !newSet[ip] {
			removed = append(removed, ip)
		}
	}
	return
}

// FormatDiff produces a human-readable diff summary.
func FormatDiff(added, removed, kept []string, hostIPs map[string]string) string {
	var b strings.Builder
	for _, ip := range added {
		fmt.Fprintf(&b, "+ %s (%s)\n", ip, hostIPs[ip])
	}
	for _, ip := range removed {
		host := hostIPs[ip]
		if host == "" {
			host = "unknown"
		}
		fmt.Fprintf(&b, "- %s (%s)\n", ip, host)
	}
	for _, ip := range kept {
		fmt.Fprintf(&b, "  %s (%s)\n", ip, hostIPs[ip])
	}
	return b.String()
}

// ExtractIPsFromItems extracts IP address strings from traffic matching list items.
func ExtractUniFiIPsFromItems(items []unifi.TrafficMatchItem) []string {
	var ips []string
	for _, item := range items {
		switch item.Type {
		case "IP_ADDRESS", "SUBNET":
			ips = append(ips, item.Value)
		}
	}
	return ips
}

// IPsToItems converts a sorted list of IPs into traffic matching list items.
func IPsToUniFiItems(ips []string) []unifi.TrafficMatchItem {
	items := make([]unifi.TrafficMatchItem, len(ips))
	for i, ip := range ips {
		if strings.Contains(ip, "/") {
			items[i] = unifi.TrafficMatchItem{Type: "SUBNET", Value: ip}
		} else {
			items[i] = unifi.TrafficMatchItem{Type: "IP_ADDRESS", Value: ip}
		}
	}
	return items
}

// ExtractNPMIPsFromItems extracts IP address strings from NPM access-list items.
func ExtractNPMIPsFromItems(items []npmclient.TrafficMatchItem) []string {
	var ips []string
	for _, item := range items {
		switch item.Type {
		case "IP_ADDRESS", "SUBNET":
			ips = append(ips, item.Value)
		}
	}
	return ips
}

// IPsToNPMItems converts sorted IPs into NPM access-list items.
func IPsToNPMItems(ips []string) []npmclient.TrafficMatchItem {
	items := make([]npmclient.TrafficMatchItem, len(ips))
	for i, ip := range ips {
		if strings.Contains(ip, "/") {
			items[i] = npmclient.TrafficMatchItem{Type: "SUBNET", Value: ip}
		} else {
			items[i] = npmclient.TrafficMatchItem{Type: "IP_ADDRESS", Value: ip}
		}
	}
	return items
}

// ExtractIPsFromItems is kept for backward compatibility and maps to UniFi item extraction.
func ExtractIPsFromItems(items []unifi.TrafficMatchItem) []string {
	return ExtractUniFiIPsFromItems(items)
}

// IPsToItems is kept for backward compatibility and maps to UniFi item conversion.
func IPsToItems(ips []string) []unifi.TrafficMatchItem {
	return IPsToUniFiItems(ips)
}

func mergeResolvedIPs(observed, current map[string]string) map[string]string {
	merged := make(map[string]string, len(observed)+len(current))
	for ip, source := range observed {
		merged[ip] = source
	}
	for ip, source := range current {
		for _, part := range strings.Split(source, ", ") {
			addResolvedSource(merged, ip, part)
		}
	}
	return merged
}

func observedIPRetention(job *store.SyncJob) time.Duration {
	ttlHours := store.DefaultObservedIPTTLHours
	if job == nil {
		return time.Duration(ttlHours) * time.Hour
	}
	if job.ObservedIPTTLHours <= 0 {
		return 0
	}
	ttlHours = job.ObservedIPTTLHours
	return time.Duration(ttlHours) * time.Hour
}

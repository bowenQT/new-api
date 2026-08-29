package upstreamprice

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
)

// Catalog status values exposed by the current-price query (spec §8.3).
const (
	CatalogStatusCurrent     = "current"
	CatalogStatusMissing     = "missing"
	CatalogStatusUnsupported = "unsupported"
	CatalogStatusRejected    = "rejected"
)

// DefaultManualStaleThresholdSeconds is the staleness threshold for manual
// sources without an explicit settings override (spec §8.3 requires manual
// sources to use an explicit threshold; this is the documented default).
const DefaultManualStaleThresholdSeconds = int64(7 * 24 * 60 * 60)

func staleThresholdSeconds(source *model.PriceSource, settings SourceSettings) int64 {
	if settings.StaleThresholdSeconds != nil {
		return *settings.StaleThresholdSeconds
	}
	if source.ScheduleEnabled && source.ScheduleIntervalSeconds > 0 {
		return 2 * source.ScheduleIntervalSeconds
	}
	return DefaultManualStaleThresholdSeconds
}

// GetCurrentUpstreamPrices projects the current price catalog (spec §8.3):
// current entries are the valid run items of each source's last successful
// run; missing entries keep their last observed snapshot but are labeled;
// unsupported/rejected entries carry their warning code and no price. It is a
// read-only projection and touches no sale-pricing configuration.
func GetCurrentUpstreamPrices(sourceId *int) (*dto.UpstreamCurrentPriceResponse, error) {
	var sources []*model.PriceSource
	if sourceId != nil {
		source, err := model.GetPriceSourceById(*sourceId)
		if err != nil {
			return nil, err
		}
		sources = []*model.PriceSource{source}
	} else {
		all, err := model.GetAllPriceSources()
		if err != nil {
			return nil, err
		}
		sources = all
	}

	now := common.GetTimestamp()
	entries, err := currentPriceEntries(sources, now)
	if err != nil {
		return nil, err
	}
	alerts, err := EvaluateSourceAlerts(sources, now)
	if err != nil {
		return nil, err
	}
	return &dto.UpstreamCurrentPriceResponse{GeneratedAt: now, Entries: entries, Alerts: alerts}, nil
}

// currentPriceEntries is the entry half of the catalog projection: it labels
// every source model of the given sources against the given time and evaluates
// no alert at all. The comparison needs exactly this half — it raises the
// source alerts itself, against its own generation timestamp — so keeping the
// two separable is what stops a caller from paying for an alert evaluation it
// discards.
func currentPriceEntries(sources []*model.PriceSource, now int64) ([]dto.UpstreamCurrentPriceEntry, error) {
	var entries []dto.UpstreamCurrentPriceEntry
	for _, source := range sources {
		sourceEntries, err := currentEntriesForSource(source, now)
		if err != nil {
			return nil, err
		}
		entries = append(entries, sourceEntries...)
	}
	return entries, nil
}

func currentEntriesForSource(source *model.PriceSource, now int64) ([]dto.UpstreamCurrentPriceEntry, error) {
	if source.LastSuccessRunId == nil {
		return nil, nil
	}
	config, err := SourceConfigFromModel(source)
	if err != nil {
		return nil, err
	}
	orphaned, err := IsPriceSourceOrphaned(source)
	if err != nil {
		return nil, err
	}
	run, err := model.GetPriceSyncRunById(*source.LastSuccessRunId)
	if err != nil {
		return nil, err
	}
	stale := false
	if run.FinishedAt != nil {
		stale = now-*run.FinishedAt > staleThresholdSeconds(source, config.Settings)
	}
	// The run's observations describe the configuration it ran under; if the
	// source has since been pointed at another channel, adapter, role, scope,
	// or settings, they are labeled and stop counting as confirmed costs.
	configChanged := priceSourceConfigChanged(config, run)
	items, err := model.GetPriceSyncRunItems(run.Id)
	if err != nil {
		return nil, err
	}

	snapshotIds := make([]int, 0, len(items))
	missingModels := make([]string, 0)
	for _, item := range items {
		if item.Status == model.PriceSyncItemStatusValid && item.SnapshotId != nil {
			snapshotIds = append(snapshotIds, *item.SnapshotId)
		}
		if item.Status == model.PriceSyncItemStatusMissing {
			missingModels = append(missingModels, item.SourceModelName)
		}
	}
	snapshots, err := model.GetPriceSnapshotsByIds(snapshotIds)
	if err != nil {
		return nil, err
	}
	snapshotById := make(map[int]*model.PriceSnapshot, len(snapshots))
	for _, snapshot := range snapshots {
		snapshotById[snapshot.Id] = snapshot
	}
	// Models the source stopped returning keep their last observed snapshot;
	// they are looked up for the whole run at once, never one query per model.
	lastSnapshotByModel, err := model.GetLatestPriceSnapshotsForModels(source.Id, missingModels)
	if err != nil {
		return nil, err
	}

	entries := make([]dto.UpstreamCurrentPriceEntry, 0, len(items))
	canonicalCounts := make(map[string]int)
	for _, item := range items {
		entry := dto.UpstreamCurrentPriceEntry{
			SourceId:            source.Id,
			SourceName:          source.Name,
			Role:                source.Role,
			Scope:               source.Scope,
			ChannelId:           source.ChannelId,
			SourceModelName:     item.SourceModelName,
			WarningCode:         item.WarningCode,
			RunId:               run.Id,
			RunFinishedAt:       run.FinishedAt,
			Stale:               stale,
			Orphaned:            orphaned,
			SourceConfigChanged: configChanged,
		}
		switch item.Status {
		case model.PriceSyncItemStatusValid:
			snapshot := snapshotById[deref(item.SnapshotId)]
			if snapshot == nil {
				continue
			}
			entry.Status = CatalogStatusCurrent
			fillEntryFromSnapshot(&entry, snapshot)
		case model.PriceSyncItemStatusMissing:
			entry.Status = CatalogStatusMissing
			if lastSnapshot := lastSnapshotByModel[item.SourceModelName]; lastSnapshot != nil {
				fillEntryFromSnapshot(&entry, lastSnapshot)
			}
		case model.PriceSyncItemStatusUnsupported:
			entry.Status = CatalogStatusUnsupported
		default:
			entry.Status = CatalogStatusRejected
		}
		if entry.CanonicalModelName != "" {
			canonicalCounts[entry.CanonicalModelName]++
		}
		entries = append(entries, entry)
	}

	// Canonical conflicts: several source models of the same source mapping
	// to one canonical name are all returned and labeled (spec §7.5).
	for i := range entries {
		if entries[i].CanonicalModelName != "" && canonicalCounts[entries[i].CanonicalModelName] > 1 {
			entries[i].CanonicalConflict = true
		}
	}
	return entries, nil
}

func fillEntryFromSnapshot(entry *dto.UpstreamCurrentPriceEntry, snapshot *model.PriceSnapshot) {
	// The snapshot's own role/scope/provider are the historical authority
	// (spec §7.1): editing a source's declaration later must never
	// reinterpret persisted observations. The source declaration is only
	// used for entries that have no snapshot (unsupported/rejected).
	entry.Role = snapshot.Role
	entry.Scope = snapshot.Scope
	entry.SnapshotId = snapshot.Id
	entry.CanonicalModelName = snapshot.CanonicalModelName
	entry.MappingStatus = snapshot.MappingStatus
	entry.Provider = snapshot.Provider
	entry.Currency = snapshot.Currency
	entry.FormulaKind = snapshot.FormulaKind
	entry.PriceExpr = snapshot.PriceExpr
	entry.ExprVersion = snapshot.ExprVersion
	entry.EffectiveAt = snapshot.EffectiveAt
	entry.FetchedAt = snapshot.FetchedAt
	entry.LastSeenAt = snapshot.LastSeenAt
	entry.Fingerprint = snapshot.Fingerprint
	if snapshot.Metadata != "" {
		metadata := map[string]string{}
		if err := common.UnmarshalJsonStr(snapshot.Metadata, &metadata); err == nil {
			entry.Metadata = metadata
			entry.VariesByProvider = metadata[MetadataKeyVariesByProvider] == "true"
		}
	}
}

func deref(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

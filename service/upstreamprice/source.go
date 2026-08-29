package upstreamprice

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
)

// Source management orchestration (spec §10.1). Controllers stay thin:
// binding and auth only; all validation lives here and in validate.go.

func applySourceRequest(target *model.PriceSource, req *dto.UpstreamPriceSourceRequest) {
	target.Name = req.Name
	target.AdapterKey = req.AdapterKey
	target.Role = req.Role
	target.Scope = req.Scope
	target.ChannelId = req.ChannelId
	if req.Enabled != nil {
		target.Enabled = *req.Enabled
	}
	if req.ScheduleEnabled != nil {
		target.ScheduleEnabled = *req.ScheduleEnabled
	}
	if req.ScheduleIntervalSeconds != nil {
		target.ScheduleIntervalSeconds = *req.ScheduleIntervalSeconds
	}
	if req.Settings != nil {
		target.Settings = *req.Settings
	}
}

// CreatePriceSource validates and persists a new source. Enabled defaults to
// true and background scheduling stays off (Phase 1).
func CreatePriceSource(req *dto.UpstreamPriceSourceRequest) (*model.PriceSource, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	source := &model.PriceSource{Enabled: true}
	applySourceRequest(source, req)
	if err := ValidatePriceSourceForWrite(source); err != nil {
		return nil, err
	}
	canonicalSettings, err := CanonicalSourceSettingsJSON(source.Settings)
	if err != nil {
		return nil, err
	}
	source.Settings = canonicalSettings
	if err := model.InsertPriceSource(source); err != nil {
		return nil, err
	}
	return source, nil
}

// UpdatePriceSource validates and persists changes to an existing source.
// Every accepted update increments config_revision, invalidating outstanding
// preview tokens (spec §8.1). Hard delete is not offered; use enabled=false.
func UpdatePriceSource(id int, req *dto.UpstreamPriceSourceRequest) (*model.PriceSource, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	source, err := model.GetPriceSourceById(id)
	if err != nil {
		return nil, err
	}
	expectedRevision := source.ConfigRevision
	applySourceRequest(source, req)
	if err := ValidatePriceSourceForWrite(source); err != nil {
		return nil, err
	}
	canonicalSettings, err := CanonicalSourceSettingsJSON(source.Settings)
	if err != nil {
		return nil, err
	}
	source.Settings = canonicalSettings
	if err := model.UpdatePriceSourceCAS(source, expectedRevision); err != nil {
		return nil, err
	}
	return source, nil
}

// ListPriceSources returns all sources annotated with their orphan state and
// the coverage / freshness aggregates of their last successful run (spec
// §8.3). The runs are loaded with a single batched query, never one per
// source.
func ListPriceSources() ([]dto.UpstreamPriceSourceView, error) {
	sources, err := model.GetAllPriceSources()
	if err != nil {
		return nil, err
	}
	runIds := make([]int, 0, len(sources))
	for _, source := range sources {
		if source.LastSuccessRunId != nil {
			runIds = append(runIds, *source.LastSuccessRunId)
		}
	}
	runs, err := model.GetPriceSyncRunsByIds(runIds)
	if err != nil {
		return nil, err
	}
	runById := make(map[int]*model.PriceSyncRun, len(runs))
	for _, run := range runs {
		runById[run.Id] = run
	}

	now := common.GetTimestamp()
	views := make([]dto.UpstreamPriceSourceView, 0, len(sources))
	for _, source := range sources {
		orphaned, err := IsPriceSourceOrphaned(source)
		if err != nil {
			return nil, err
		}
		config, err := SourceConfigFromModel(source)
		if err != nil {
			return nil, err
		}
		view := sourceView(source, orphaned)
		if source.LastSuccessRunId != nil {
			if run := runById[*source.LastSuccessRunId]; run != nil {
				view.LastSuccessFinishedAt = run.FinishedAt
				view.CoverageCount = run.ValidCount
				view.MissingCount = run.MissingCount
				view.Stale = sourceStale(source, config.Settings, run, now)
			}
		}
		views = append(views, view)
	}
	return views, nil
}

func sourceView(source *model.PriceSource, orphaned bool) dto.UpstreamPriceSourceView {
	return dto.UpstreamPriceSourceView{
		Id:                      source.Id,
		Name:                    source.Name,
		AdapterKey:              source.AdapterKey,
		Role:                    source.Role,
		Scope:                   source.Scope,
		ChannelId:               source.ChannelId,
		Enabled:                 source.Enabled,
		ScheduleEnabled:         source.ScheduleEnabled,
		ScheduleIntervalSeconds: source.ScheduleIntervalSeconds,
		Settings:                source.Settings,
		ConfigRevision:          source.ConfigRevision,
		LastSuccessRunId:        source.LastSuccessRunId,
		LastSuccessAt:           source.LastSuccessAt,
		LastErrorAt:             source.LastErrorAt,
		LastErrorSummary:        source.LastErrorSummary,
		Orphaned:                orphaned,
		CreatedTime:             source.CreatedTime,
		UpdatedTime:             source.UpdatedTime,
	}
}

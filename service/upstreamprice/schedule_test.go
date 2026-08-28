package upstreamprice

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var scheduleTestAdapterCounter int64

// scheduleTestAdapter is a registry stub: the scheduling tests exercise source
// validation and due selection, never a fetch.
type scheduleTestAdapter struct{ key string }

func (a scheduleTestAdapter) Key() string { return a.key }

func (a scheduleTestAdapter) Supports(source SourceConfig) bool { return source.AdapterKey == a.key }

func (a scheduleTestAdapter) Fetch(context.Context, SourceConfig) ([]Observation, FetchMeta, error) {
	return nil, FetchMeta{}, errors.New("scheduleTestAdapter does not fetch")
}

func (a scheduleTestAdapter) AllowedRoles() []PriceRole {
	return []PriceRole{RoleCuratedReference}
}

func (a scheduleTestAdapter) AllowedScopes() []PriceScope {
	return []PriceScope{ScopeUnknown}
}

func registerScheduleTestAdapter(t *testing.T) string {
	t.Helper()
	key := fmt.Sprintf("schedule_test_%d", atomic.AddInt64(&scheduleTestAdapterCounter, 1))
	require.NoError(t, RegisterAdapter(scheduleTestAdapter{key: key}))
	return key
}

// TestScheduledSourceDue pins the §8.4 selection rules: only enabled and
// scheduled sources run, the six-hour floor is enforced at selection time as
// well as at write time, and a failed attempt backs off for a full interval
// instead of retrying on the next wake.
func TestScheduledSourceDue(t *testing.T) {
	const now = int64(1_000_000_000)
	sixHours := MinScheduleIntervalSeconds

	cases := []struct {
		name   string
		source model.PriceSource
		want   bool
	}{
		{
			name:   "never attempted source is due",
			source: model.PriceSource{Enabled: true, ScheduleEnabled: true, ScheduleIntervalSeconds: sixHours},
			want:   true,
		},
		{
			name:   "scheduling disabled",
			source: model.PriceSource{Enabled: true, ScheduleEnabled: false, ScheduleIntervalSeconds: sixHours},
			want:   false,
		},
		{
			name:   "source disabled",
			source: model.PriceSource{Enabled: false, ScheduleEnabled: true, ScheduleIntervalSeconds: sixHours},
			want:   false,
		},
		{
			name:   "interval below the six hour floor never runs",
			source: model.PriceSource{Enabled: true, ScheduleEnabled: true, ScheduleIntervalSeconds: sixHours - 1},
			want:   false,
		},
		{
			name:   "zero interval never runs",
			source: model.PriceSource{Enabled: true, ScheduleEnabled: true, ScheduleIntervalSeconds: 0},
			want:   false,
		},
		{
			name: "recent success is not due",
			source: model.PriceSource{
				Enabled: true, ScheduleEnabled: true, ScheduleIntervalSeconds: sixHours,
				LastSuccessAt: int64Ptr(now - sixHours + 1),
			},
			want: false,
		},
		{
			name: "elapsed interval after success is due",
			source: model.PriceSource{
				Enabled: true, ScheduleEnabled: true, ScheduleIntervalSeconds: sixHours,
				LastSuccessAt: int64Ptr(now - sixHours),
			},
			want: true,
		},
		{
			name: "recent failure backs off for a full interval",
			source: model.PriceSource{
				Enabled: true, ScheduleEnabled: true, ScheduleIntervalSeconds: sixHours,
				LastSuccessAt: int64Ptr(now - 10*sixHours),
				LastErrorAt:   int64Ptr(now - 60),
			},
			want: false,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			source := testCase.source
			assert.Equal(t, testCase.want, scheduledSourceDue(&source, now))
		})
	}
}

// TestScheduledSyncEnabledDefaultsOff covers the spec §8.4 requirement that
// background sync stays off unless the deployment switch is set, even when a
// source is scheduled.
func TestScheduledSyncEnabledDefaultsOff(t *testing.T) {
	db := setupCompareTestDB(t)
	source := createAlertTestSource(t, RoleCuratedReference, nil, "")
	require.NoError(t, db.Model(&model.PriceSource{}).Where("id = ?", source.Id).Updates(map[string]interface{}{
		"schedule_enabled":          true,
		"schedule_interval_seconds": MinScheduleIntervalSeconds,
	}).Error)

	assert.False(t, ScheduledSyncEnabled())

	t.Setenv(ScheduleTaskEnabledEnvKey, "true")
	assert.True(t, ScheduledSyncEnabled())

	require.NoError(t, db.Model(&model.PriceSource{}).Where("id = ?", source.Id).
		Update("schedule_enabled", false).Error)
	assert.False(t, ScheduledSyncEnabled())
}

// TestValidateScheduleInterval pins the write-side gate: scheduling cannot be
// enabled below the six-hour minimum (spec §8.4).
func TestValidateScheduleInterval(t *testing.T) {
	setupCompareTestDB(t)
	adapterKey := registerScheduleTestAdapter(t)

	enabled := true
	base := func(interval int64) *dto.UpstreamPriceSourceRequest {
		return &dto.UpstreamPriceSourceRequest{
			Name:                    "reference",
			AdapterKey:              adapterKey,
			Role:                    string(RoleCuratedReference),
			Scope:                   string(ScopeUnknown),
			ScheduleEnabled:         &enabled,
			ScheduleIntervalSeconds: &interval,
		}
	}

	_, err := CreatePriceSource(base(MinScheduleIntervalSeconds - 1))
	require.ErrorContains(t, err, "schedule_interval_seconds must be at least")

	zero := int64(0)
	_, err = CreatePriceSource(&dto.UpstreamPriceSourceRequest{
		Name:                    "reference",
		AdapterKey:              adapterKey,
		Role:                    string(RoleCuratedReference),
		Scope:                   string(ScopeUnknown),
		ScheduleEnabled:         &enabled,
		ScheduleIntervalSeconds: &zero,
	})
	require.ErrorContains(t, err, "scheduled sync requires")

	source, err := CreatePriceSource(base(MinScheduleIntervalSeconds))
	require.NoError(t, err)
	assert.True(t, source.ScheduleEnabled)
	assert.Equal(t, MinScheduleIntervalSeconds, source.ScheduleIntervalSeconds)
}

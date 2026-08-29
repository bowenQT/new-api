package controller

import (
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/service/upstreamprice"

	// Register concrete price adapters (Vercel AI Gateway) with the registry.
	_ "github.com/QuantumNous/new-api/service/upstreamprice/adapters"

	"github.com/gin-gonic/gin"
)

// Upstream price catalog controllers (spec §10). All routes are RootAuth-only
// and never touch sale-pricing configuration; vendor parsing lives in the
// adapter registry, not here.

// GetUpstreamPriceAdapters lists the registered adapters with the roles,
// scopes, channel requirement, and pinned endpoint each one accepts, so the
// admin UI configures sources from the registry rather than a hardcoded copy.
func GetUpstreamPriceAdapters(c *gin.Context) {
	common.ApiSuccess(c, upstreamprice.ListAdapters())
}

func GetUpstreamPriceSources(c *gin.Context) {
	views, err := upstreamprice.ListPriceSources()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, views)
}

// GetUpstreamPriceSourceAlerts returns the source-level catalog health alerts
// (spec §13) without the catalog projection, so the source list page can render
// them without asking for every model's current price.
func GetUpstreamPriceSourceAlerts(c *gin.Context) {
	alerts, err := upstreamprice.ListSourceAlerts()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, alerts)
}

func CreateUpstreamPriceSource(c *gin.Context) {
	request := dto.UpstreamPriceSourceRequest{}
	if err := c.ShouldBindJSON(&request); err != nil {
		common.ApiError(c, err)
		return
	}
	source, err := upstreamprice.CreatePriceSource(&request)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, source)
}

func UpdateUpstreamPriceSource(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiErrorMsg(c, "invalid source id")
		return
	}
	request := dto.UpstreamPriceSourceRequest{}
	if err := c.ShouldBindJSON(&request); err != nil {
		common.ApiError(c, err)
		return
	}
	source, err := upstreamprice.UpdatePriceSource(id, &request)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, source)
}

func PreviewUpstreamPriceSource(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiErrorMsg(c, "invalid source id")
		return
	}
	preview, err := upstreamprice.PreviewPriceSource(c.Request.Context(), id)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, preview)
}

func SyncUpstreamPriceSource(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiErrorMsg(c, "invalid source id")
		return
	}
	request := dto.UpstreamPriceSyncRequest{}
	if err := c.ShouldBindJSON(&request); err != nil {
		common.ApiError(c, err)
		return
	}
	if err := request.Validate(); err != nil {
		common.ApiError(c, err)
		return
	}
	result, err := upstreamprice.SyncPriceSource(c.Request.Context(), id, request.PreviewToken)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, result)
}

func GetCurrentUpstreamPrices(c *gin.Context) {
	var sourceId *int
	if raw := c.Query("source_id"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			common.ApiErrorMsg(c, "invalid source_id")
			return
		}
		sourceId = &parsed
	}
	catalog, err := upstreamprice.GetCurrentUpstreamPrices(sourceId)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, catalog)
}

// CompareUpstreamPrices returns the estimate-only cost / sale price / margin
// comparison (spec §9.2, §10.3). It writes no state.
func CompareUpstreamPrices(c *gin.Context) {
	request := dto.UpstreamPriceCompareRequest{}
	if err := c.ShouldBindJSON(&request); err != nil {
		common.ApiError(c, err)
		return
	}
	comparison, err := upstreamprice.CompareUpstreamPrices(&request)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, comparison)
}

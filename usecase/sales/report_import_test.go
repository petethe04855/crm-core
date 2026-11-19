package sales

import (
	"testing"

	"crm-core/domain"
	domainsales "crm-core/domain/sales"

	"github.com/stretchr/testify/require"
)

// reportHTML wraps JSON in the script tag the exported report uses.
func reportHTML(json string) []byte {
	return []byte(`<!doctype html><html><body><script id="analytics-data" type="application/json">` + json + `</script></body></html>`)
}

func TestParseReportFromExportedHTML(t *testing.T) {
	data := reportHTML(`{"all_orders":[
		{"id":"250101ABC","date":"2026-11-02 14:30:00","amount":199.5,"buyer":"alice","platform":"shopee","channel":"Shopee","channel_type":"shopee","province":"Bangkok","sets":["Set A"],"items":["Food A"],"chicken_bags":3,"tuna_bags":1,"rabbit_bags":0,"total_bags":4,"accessory_units":{"cat_wand":1}},
		{"id":2501010002,"date":"2026-11-03","amount":100,"buyer":"bob","platform":"tiktok","channel":"TikTok","channel_type":"tiktok_live","province":"Chiang Mai","total_bags":2}
	]}`)

	orders, err := ParseReport(data)
	require.NoError(t, err)
	require.Len(t, orders, 2)

	first := orders[0]
	require.Equal(t, domainsales.PlatformShopee, first.Platform)
	require.Equal(t, "250101ABC", first.OrderNo)
	require.Equal(t, "2026-11-02 14:30:00", first.OrderedAt)
	require.True(t, first.HasTime)
	require.Equal(t, "2026-11-02", first.DateDay)
	require.Equal(t, 199.5, first.Amount)
	require.Equal(t, "alice", first.Buyer)
	require.EqualValues(t, 4, first.TotalBags)
	require.EqualValues(t, 1, first.CatWand)

	second := orders[1]
	require.Equal(t, "2501010002", second.OrderNo, "numeric ids are accepted")
	require.Equal(t, domainsales.PlatformTikTok, second.Platform)
	require.Equal(t, domainsales.ChannelTikTokLive, second.ChannelType)
	require.Equal(t, "2026-11-03", second.DateDay)
	require.Equal(t, 2, second.TotalBags)
}

func TestParseReportRejectsBadInput(t *testing.T) {
	_, err := ParseReport([]byte("not a report"))
	require.ErrorIs(t, err, domain.ErrValidation)

	_, err = ParseReport(reportHTML(`{"all_orders":[]}`))
	require.Error(t, err, "an empty report is not a usable snapshot")

	// The same order id twice would double every number.
	dup := `{"all_orders":[
		{"id":"X1","date":"2026-11-02","amount":10,"buyer":"a","platform":"shopee","channel_type":"shopee","total_bags":1},
		{"id":"X1","date":"2026-11-03","amount":10,"buyer":"a","platform":"shopee","channel_type":"shopee","total_bags":1}
	]}`
	_, err = ParseReport(reportHTML(dup))
	require.Error(t, err)

	// A channel type that disagrees with the platform is a corrupt export.
	bad := `{"all_orders":[{"id":"X2","date":"2026-11-02","amount":10,"buyer":"a","platform":"tiktok","channel_type":"shopee","total_bags":1}]}`
	_, err = ParseReport(reportHTML(bad))
	require.Error(t, err)
}

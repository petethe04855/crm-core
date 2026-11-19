package sales

import (
	"testing"
	"time"

	domainsales "crm-core/domain/sales"

	"github.com/stretchr/testify/require"
)

func at(v string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04:05", v, Bangkok)
	if err != nil {
		panic(err)
	}
	return t
}

func order(platform, no, orderedAt, buyer, province string, amount float64) domainsales.SalesOrder {
	return domainsales.SalesOrder{
		Platform:  platform,
		OrderNo:   no,
		OrderedAt: orderedAt,
		HasTime:   true,
		BuyerID:   buyer,
		Buyer:     buyer,
		DateDay:   orderedAt[:10],
		Amount:    amount,
		Province:  province,
	}
}

func november() Options {
	return Options{From: at("2026-11-01 00:00:00"), To: at("2026-12-01 00:00:00")}
}

func TestBuildFiltersByBangkokDayBoundary(t *testing.T) {
	first := order(domainsales.PlatformShopee, "A", "2026-11-01 00:30:00", "alice", "Bangkok", 100)
	first.ChickenBags, first.TotalBags = 5, 5
	last := order(domainsales.PlatformShopee, "B", "2026-10-31 23:59:59", "bob", "Bangkok", 999)
	next := order(domainsales.PlatformShopee, "C", "2026-12-01 00:00:00", "carol", "Bangkok", 999)

	report := Build([]domainsales.SalesOrder{first, last, next}, nil, november())

	require.EqualValues(t, 1, report.Summary.Orders, "only the 1 Nov 00:30 order belongs to November")
	require.Equal(t, "2026-11-01", report.From)
	require.Equal(t, "2026-11-30", report.To)
}

func TestBuildSummaryChannelsAndTrend(t *testing.T) {
	a := order(domainsales.PlatformShopee, "A", "2026-11-01 10:00:00", "alice", "Bangkok", 100)
	a.ChickenBags, a.TotalBags = 5, 5
	c := order(domainsales.PlatformShopee, "C", "2026-11-15 12:00:00", "alice", "Bangkok", 300)
	c.ChickenBags, c.TotalBags, c.CatWand = 7, 7, 2
	c.Sets = []string{"Set A"}
	b := order(domainsales.PlatformTikTok, "B", "2026-11-20 20:00:00", "bob", "Chiang Mai", 200)
	b.TunaBags, b.TotalBags = 10, 10

	report := Build([]domainsales.SalesOrder{a, b, c}, nil, november())

	require.EqualValues(t, 3, report.Summary.Orders)
	require.Equal(t, 600.0, report.Summary.Sales)
	require.EqualValues(t, 2, report.Summary.Buyers, "bob and alice")
	require.Equal(t, 200.0, report.Summary.AOV)
	require.EqualValues(t, 22, report.Summary.Items)

	require.Len(t, report.Channels, 2)
	require.Equal(t, domainsales.ChannelShopee, report.Channels[0].Channel)
	require.Equal(t, 400.0, report.Channels[0].Sales)
	require.Equal(t, 66.67, report.Channels[0].SharePercent)

	require.Len(t, report.Trend.Monthly, 1)
	require.Equal(t, "2026-11", report.Trend.Monthly[0].Period)
	require.Equal(t, 400.0, report.Trend.Monthly[0].ShopeeSales)
	require.Equal(t, 200.0, report.Trend.Monthly[0].TikTokSales)
	require.EqualValues(t, 3, report.Trend.Monthly[0].TotalOrders)
	require.Len(t, report.Trend.Weekly, 3, "1 Nov, 15 Nov and 20 Nov fall in three ISO weeks")
}

func TestBuildFiltersChannelAndRegion(t *testing.T) {
	bkk := order(domainsales.PlatformShopee, "A", "2026-11-02 10:00:00", "alice", "Bangkok", 100)
	bkk.ChickenBags, bkk.TotalBags = 5, 5
	cnx := order(domainsales.PlatformTikTok, "B", "2026-11-03 10:00:00", "bob", "Chiang Mai", 200)
	cnx.TunaBags, cnx.TotalBags = 5, 5

	opts := november()
	opts.Channel = "shopee"
	require.EqualValues(t, 1, Build([]domainsales.SalesOrder{bkk, cnx}, nil, opts).Summary.Orders)

	opts = november()
	opts.Region = domainsales.RegionBangkokMetro
	report := Build([]domainsales.SalesOrder{bkk, cnx}, nil, opts)
	require.EqualValues(t, 1, report.Summary.Orders)
	require.Equal(t, domainsales.NormalizeProvince("Bangkok", ""), report.Provinces[0].Province)
	require.Equal(t, domainsales.RegionBangkokMetro, report.Provinces[0].Region)
}

func TestBuildUnknownProvinceIsGroupedNotDropped(t *testing.T) {
	o := order(domainsales.PlatformShopee, "A", "2026-11-02 10:00:00", "alice", "Atlantis", 100)
	o.TotalBags = 1
	o.ChickenBags = 1

	report := Build([]domainsales.SalesOrder{o}, nil, november())

	require.Len(t, report.Provinces, 1)
	require.Equal(t, domainsales.UnknownProvince, report.Provinces[0].Province)
	require.Equal(t, domainsales.RegionUnknown, report.Provinces[0].Region)
	require.Len(t, report.Regions, 1)
	require.Equal(t, domainsales.RegionUnknown, report.Regions[0].Region)
}

func TestBuildMasksBuyersUnlessOwner(t *testing.T) {
	o := order(domainsales.PlatformShopee, "A", "2026-11-02 10:00:00", "alice1234", "Bangkok", 100)
	o.ChickenBags, o.TotalBags = 1, 1

	masked := Build([]domainsales.SalesOrder{o}, nil, november())
	require.True(t, masked.Masked)
	require.Equal(t, "al***34", masked.Customers[0].Buyer)

	opts := november()
	opts.Unmasked = true
	open := Build([]domainsales.SalesOrder{o}, nil, opts)
	require.False(t, open.Masked)
	require.Equal(t, "alice1234", open.Customers[0].Buyer)
}

func TestBuildFlavorsAndAddOns(t *testing.T) {
	a := order(domainsales.PlatformShopee, "A", "2026-11-02 10:00:00", "alice", "Bangkok", 100)
	a.ChickenBags, a.TunaBags, a.TotalBags = 6, 2, 8
	a.Sets = []string{"Set A"}
	a.CatWand, a.Catnip = 1, 3
	b := order(domainsales.PlatformShopee, "B", "2026-11-03 10:00:00", "bob", "Bangkok", 100)
	b.ChickenBags, b.TotalBags = 2, 2
	b.Sets = []string{"Set A"}

	report := Build([]domainsales.SalesOrder{a, b}, nil, november())

	require.Len(t, report.Flavors, 2)
	require.Equal(t, domainsales.FlavorChicken, report.Flavors[0].Key)
	require.EqualValues(t, 8, report.Flavors[0].Units)
	require.EqualValues(t, 2, report.Flavors[0].Orders)
	require.Equal(t, 80.0, report.Flavors[0].SharePercent)
	require.Equal(t, domainsales.FlavorTuna, report.Flavors[1].Key)
	require.EqualValues(t, 2, report.Flavors[1].Units)

	require.Len(t, report.Sets, 1)
	require.Equal(t, "Set A", report.Sets[0].Name)
	require.EqualValues(t, 2, report.Sets[0].Orders)
	require.Equal(t, 100.0, report.Sets[0].SharePercent)

	require.Len(t, report.Accessories, 3)
	require.EqualValues(t, 1, report.Accessories[0].Units)
	require.EqualValues(t, 3, report.Accessories[2].Units)
}

func TestBuildProductsFromOrderLines(t *testing.T) {
	a := order(domainsales.PlatformShopee, "A", "2026-11-02 10:00:00", "alice", "Bangkok", 100)
	a.Lines = []domainsales.OrderLine{
		{SKU: "SKU-1", Name: "Food A", Qty: 3, Amount: 60, Flavor: domainsales.FlavorChicken},
		{SKU: "SKU-2", Name: "Toy", Qty: 1, Amount: 40},
	}
	b := order(domainsales.PlatformShopee, "B", "2026-11-03 10:00:00", "bob", "Bangkok", 200)
	b.Lines = []domainsales.OrderLine{
		{SKU: "SKU-1", Name: "Food A", Qty: 2, Amount: 200, Flavor: domainsales.FlavorChicken},
	}

	report := Build([]domainsales.SalesOrder{a, b}, nil, november())

	require.Len(t, report.Products, 2)
	require.Equal(t, "SKU-1", report.Products[0].SKU)
	require.EqualValues(t, 5, report.Products[0].Units)
	require.EqualValues(t, 2, report.Products[0].Orders)
	require.Equal(t, 260.0, report.Products[0].Sales)
	require.Equal(t, 83.33, report.Products[0].SharePercent)

	require.Equal(t, domainsales.FlavorChicken, report.Flavors[0].Key)
	require.EqualValues(t, 5, report.Flavors[0].Units)
}

func TestBuildRepurchaseAndFrequency(t *testing.T) {
	orders := []domainsales.SalesOrder{
		order(domainsales.PlatformShopee, "A", "2026-11-01 10:00:00", "alice", "Bangkok", 100),
		order(domainsales.PlatformShopee, "B", "2026-11-10 10:00:00", "alice", "Bangkok", 100),
		order(domainsales.PlatformShopee, "C", "2026-11-20 10:00:00", "alice", "Bangkok", 100),
		order(domainsales.PlatformTikTok, "D", "2026-11-05 10:00:00", "bob", "Bangkok", 100),
	}
	for i := range orders {
		orders[i].ChickenBags, orders[i].TotalBags = 1, 1
	}

	report := Build(orders, nil, november())

	require.EqualValues(t, 1, report.Repurchase.RepeatBuyers)
	require.Equal(t, 50.0, report.Repurchase.RepeatRatePercent)
	require.Equal(t, 9.5, report.Repurchase.AverageGapDays, "(9 + 10) / 2")
	require.EqualValues(t, 2, report.Repurchase.Buckets[1].Buyers, "both gaps fall in 8-30 days")

	byLabel := map[string]int64{}
	for _, f := range report.Frequencies {
		byLabel[f.Label] = f.Buyers
	}
	require.EqualValues(t, 1, byLabel["1 ครั้ง"])
	require.EqualValues(t, 1, byLabel["2–3 ครั้ง"])
	require.Len(t, report.Customers, 2)
	require.EqualValues(t, 3, report.Customers[0].Orders, "alice ranks first by sales")
	require.Equal(t, "2–3 ครั้ง", report.Customers[0].FrequencyBucket)
}

func TestBuildComparesPreviousPeriod(t *testing.T) {
	cur := order(domainsales.PlatformShopee, "A", "2026-11-02 10:00:00", "alice", "Bangkok", 300)
	cur.ChickenBags, cur.TotalBags = 3, 3
	prev := order(domainsales.PlatformShopee, "B", "2026-10-02 10:00:00", "bob", "Bangkok", 100)
	prev.ChickenBags, prev.TotalBags = 1, 1

	from, to := november().From, november().To
	prevFrom, prevTo := PreviousRange(from, to)
	require.Equal(t, prevFrom, at("2026-10-02 00:00:00"))
	require.Equal(t, prevTo, from)

	report := Build([]domainsales.SalesOrder{cur}, []domainsales.SalesOrder{prev}, november())
	require.EqualValues(t, 1, report.Previous.Orders)
	require.Equal(t, 100.0, report.Previous.Sales)
	require.NotNil(t, report.Flavors[0].ChangePercent)
	require.Equal(t, 200.0, *report.Flavors[0].ChangePercent, "3 bags vs 1 bag = +200%")
}

func TestBuildTimingUsesOrderClock(t *testing.T) {
	a := order(domainsales.PlatformShopee, "A", "2026-11-02 14:30:00", "alice", "Bangkok", 100)
	a.ChickenBags, a.TotalBags = 1, 1
	b := order(domainsales.PlatformShopee, "B", "2026-11-02 14:45:00", "bob", "Bangkok", 100)
	b.ChickenBags, b.TotalBags = 1, 1

	report := Build([]domainsales.SalesOrder{a, b}, nil, november())

	require.Len(t, report.Timing.Hours, 24)
	require.EqualValues(t, 2, report.Timing.Hours[14].Orders)
	require.Equal(t, 14, report.Timing.PeakHour)
	require.Equal(t, "จันทร์", report.Timing.PeakWeekday)
	require.Equal(t, 2, report.Timing.PeakDay)
}

func TestPreviousRangeHandlesReversedInput(t *testing.T) {
	from, to := at("2026-11-01 00:00:00"), at("2026-11-01 00:00:00")
	prevFrom, prevTo := PreviousRange(to, from)
	require.Equal(t, from, prevTo)
	require.Equal(t, from, prevFrom)
}

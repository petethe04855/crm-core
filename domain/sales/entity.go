// Package sales holds the marketplace sales facts the CRM dashboard reads and
// the report shapes it returns. ERP owns orders; crm-core only aggregates the
// facts it is given (plan: CRM_BACKOFFICE_MARKETING.md §6).
package sales

import (
	"context"
	"time"
)

// Platforms a buyer can come from.
const (
	PlatformShopee = "shopee"
	PlatformTikTok = "tiktok"
)

// Channels are the display channels of the report. TikTok orders rarely carry
// the content type, so ChannelTikTok is the fallback for them.
const (
	ChannelShopee        = "shopee"
	ChannelTikTok        = "tiktok"
	ChannelTikTokLive    = "tiktok_live"
	ChannelTikTokProduct = "tiktok_product"
	ChannelTikTokVideo   = "tiktok_video"
)

// ChannelLabels are the labels shown in the backoffice.
var ChannelLabels = map[string]string{
	ChannelShopee:        "Shopee",
	ChannelTikTok:        "TikTok",
	ChannelTikTokLive:    "TikTok Live",
	ChannelTikTokProduct: "TikTok สินค้า",
	ChannelTikTokVideo:   "TikTok วิดีโอ",
}

// Food flavors. Orders that do not resolve to one count as unknown.
const (
	FlavorChicken = "chicken"
	FlavorTuna    = "tuna"
	FlavorRabbit  = "rabbit"
)

// FlavorLabels maps a flavor key to its Thai label.
var FlavorLabels = map[string]string{
	FlavorChicken: "ไก่",
	FlavorTuna:    "ทูน่า",
	FlavorRabbit:  "กระต่าย",
}

// Accessory kinds reported by the reference report.
const (
	AccessoryCatWand         = "cat_wand"
	AccessoryReplacementHead = "replacement_head"
	AccessoryCatnip          = "catnip"
)

// AccessoryLabels maps an accessory key to its label.
var AccessoryLabels = map[string]string{
	AccessoryCatWand:         "ไม้ตกแมว",
	AccessoryReplacementHead: "หัวเปลี่ยน",
	AccessoryCatnip:          "แคทนีพ",
}

// OrderLine is one product line of an order. The ERP endpoint fills it; the
// historical report importer leaves it empty and only fills the aggregates.
type OrderLine struct {
	SKU    string
	Name   string
	Qty    int
	Amount float64
	Flavor string // chicken / tuna / rabbit / "" when not mapped
}

// SalesOrder is one marketplace order fact. PDPA: it never carries recipient
// names or addresses; Buyer is the platform username only.
type SalesOrder struct {
	Platform        string
	OrderNo         string
	OrderedAt       string // "2006-01-02 15:04:05" in Bangkok time
	HasTime         bool
	BuyerID         string
	DateDay         string // "2006-01-02"
	Amount          float64
	Buyer           string
	Channel         string
	ChannelType     string
	Province        string
	Sets            []string
	Items           []string
	ChickenBags     int
	TunaBags        int
	RabbitBags      int
	TotalBags       int
	CatWand         int
	ReplacementHead int
	Catnip          int
	Lines           []OrderLine
}

// ChannelOf returns the report channel of an order.
func (o SalesOrder) ChannelOf() string {
	switch o.ChannelType {
	case ChannelShopee, ChannelTikTokLive, ChannelTikTokProduct, ChannelTikTokVideo, ChannelTikTok:
		return o.ChannelType
	}
	if o.Platform == PlatformShopee {
		return ChannelShopee
	}
	return ChannelTikTok
}

// ChannelLabel returns the display label of the order channel.
func (o SalesOrder) ChannelLabel() string {
	if l, ok := ChannelLabels[o.ChannelOf()]; ok {
		return l
	}
	return o.ChannelOf()
}

// Query filters the orders fetched for the dashboard. To is exclusive.
type Query struct {
	From    time.Time
	To      time.Time
	Channel string // "" or "all" means every channel
	Region  string // "" means every region
}

// OrderSource fetches order facts for a range. backoffice-api implements it by
// calling erp-api-v2; tests use a fake.
type OrderSource interface {
	Orders(ctx context.Context, q Query) ([]SalesOrder, error)
}

// Summary is the KPI block of a period.
type Summary struct {
	Sales  float64
	Orders int64
	Buyers int64
	Items  int64
	AOV    float64
}

// ChannelPoint is one channel's contribution to the period.
type ChannelPoint struct {
	Channel      string
	Label        string
	Orders       int64
	Sales        float64
	SharePercent float64
}

// TrendPoint is one week or month of sales per platform.
type TrendPoint struct {
	Period       string
	StartDate    string
	EndDate      string
	ShopeeSales  float64
	TikTokSales  float64
	TotalSales   float64
	ShopeeOrders int64
	TikTokOrders int64
	TotalOrders  int64
}

// Trend holds the weekly and monthly series.
type Trend struct {
	Weekly  []TrendPoint
	Monthly []TrendPoint
}

// MonthValue is one month of a product or flavor series.
type MonthValue struct {
	Month        string
	Units        int64
	Orders       int64
	Sales        float64
	SharePercent float64
}

// FlavorRow is one food flavor in the product ranking.
type FlavorRow struct {
	Key           string
	Label         string
	Units         int64
	Orders        int64
	Sales         float64
	SharePercent  float64
	ChangePercent *float64
	Months        []MonthValue
}

// ProductRow is one SKU in the product ranking (when the source sends lines).
type ProductRow struct {
	SKU          string
	Name         string
	Units        int64
	Orders       int64
	Sales        float64
	SharePercent float64
}

// SetRow is a set that sold in the period.
type SetRow struct {
	Name         string
	Orders       int64
	SharePercent float64
}

// AccessoryRow is an add-on sold with the food.
type AccessoryRow struct {
	Key          string
	Label        string
	Units        int64
	SharePercent float64
}

// ProvinceRow is one province of the geographic report.
type ProvinceRow struct {
	Province     string
	Region       string
	Buyers       int64
	Orders       int64
	Sales        float64
	AOV          float64
	SharePercent float64
}

// RegionRow is the roll-up of the six Thai regions.
type RegionRow struct {
	Region        string
	ProvinceCount int
	Orders        int64
	Sales         float64
	SharePercent  float64
}

// CustomerRow is one platform buyer. Buyer is masked unless the caller may see
// personal data.
type CustomerRow struct {
	Platform        string
	Buyer           string
	Orders          int64
	Sales           float64
	AOV             float64
	Items           int64
	FirstOrder      string
	LastOrder       string
	Province        string
	FrequencyBucket string
}

// CustomerFrequency is one bucket of the purchase-frequency distribution.
type CustomerFrequency struct {
	Label        string
	Buyers       int64
	SharePercent float64
}

// HourRow is one hour of the day.
type HourRow struct {
	Hour   int
	Orders int64
	Sales  float64
}

// WeekdayRow is one day of the week (1 = Monday).
type WeekdayRow struct {
	Day    int
	Label  string
	Orders int64
	Sales  float64
}

// DayOfMonthRow is one calendar day.
type DayOfMonthRow struct {
	Day    int
	Orders int64
	Sales  float64
}

// Timing groups the order-time distributions.
type Timing struct {
	Hours       []HourRow
	Weekdays    []WeekdayRow
	DaysOfMonth []DayOfMonthRow
	PeakHour    int
	PeakWeekday string
	PeakDay     int
}

// RepurchaseBucket is one gap bucket between two orders of a buyer.
type RepurchaseBucket struct {
	Label        string
	MinDays      int
	MaxDays      int
	Buyers       int64
	SharePercent float64
}

// Repurchase summarises how quickly buyers come back.
type Repurchase struct {
	Buckets           []RepurchaseBucket
	RepeatBuyers      int64
	RepeatRatePercent float64
	AverageGapDays    float64
}

// Report is the whole dashboard payload for one filter set.
type Report struct {
	From        string
	To          string
	Channel     string
	Region      string
	Masked      bool
	Summary     Summary
	Previous    *Summary
	Channels    []ChannelPoint
	Trend       Trend
	Flavors     []FlavorRow
	Products    []ProductRow
	Sets        []SetRow
	Accessories []AccessoryRow
	Provinces   []ProvinceRow
	Regions     []RegionRow
	Customers   []CustomerRow
	Frequencies []CustomerFrequency
	Timing      Timing
	Repurchase  Repurchase
}

package sales

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"crm-core/domain"
	domainsales "crm-core/domain/sales"
)

// Bangkok is the business time zone. Thailand has no DST, so a fixed zone
// avoids depending on tz files in every deployment.
var Bangkok = time.FixedZone("Asia/Bangkok", 7*60*60)

// DefaultMaxCustomers caps the customer leaderboard payload.
const DefaultMaxCustomers = 50

// Options control one dashboard report.
type Options struct {
	From, To time.Time
	Channel  string
	Region   string
	// Unmasked disables buyer masking. The zero value masks personal data, so a
	// caller that forgets to pass an actor still cannot leak usernames (PDPA).
	Unmasked         bool
	CustomerPlatform string
	CustomerBuyer    string
	MaxCustomers     int
	// mask is derived from Unmasked by Build.
	mask bool
}

// Build computes the dashboard report from the current and previous-period
// order facts. It never mutates its inputs and holds no state.
func Build(current, previous []domainsales.SalesOrder, opts Options) domainsales.Report {
	current = filterOrders(current, opts, true)
	// The caller already fetched the previous period, so only the channel and
	// region filters apply to it.
	previous = filterOrders(previous, opts, false)

	if opts.MaxCustomers <= 0 {
		opts.MaxCustomers = DefaultMaxCustomers
	}
	opts.mask = !opts.Unmasked

	cur := summarize(current)
	prev := summarize(previous)

	report := domainsales.Report{
		From:     opts.From.In(Bangkok).Format("2006-01-02"),
		To:       opts.To.AddDate(0, 0, -1).In(Bangkok).Format("2006-01-02"),
		Channel:  normalizeChannel(opts.Channel),
		Region:   strings.TrimSpace(opts.Region),
		Masked:   opts.mask,
		Summary:  cur,
		Previous: &prev,
	}
	report.Channels = buildChannels(current)
	report.Trend = buildTrend(current, opts)
	report.Flavors = buildFlavors(current, previous, opts)
	report.Products = buildProducts(current)
	report.Sets, report.Accessories = buildAddOns(current)
	report.Provinces = buildProvinces(current)
	report.Regions = buildRegions(report.Provinces)
	report.Customers = buildCustomers(current, opts)
	report.Frequencies = buildFrequencies(current)
	report.Timing = buildTiming(current)
	report.Repurchase = buildRepurchase(current)
	return report
}

// PreviousRange returns the equal-length period immediately before [from, to).
func PreviousRange(from, to time.Time) (time.Time, time.Time) {
	span := to.Sub(from)
	if span < 0 {
		span = -span
	}
	return from.Add(-span), from
}

func normalizeChannel(channel string) string {
	c := strings.ToLower(strings.TrimSpace(channel))
	if c == "all" {
		return ""
	}
	return c
}

// filterOrders applies the channel and region filters, plus the date range
// when checkRange is set.
func filterOrders(orders []domainsales.SalesOrder, opts Options, checkRange bool) []domainsales.SalesOrder {
	channel := normalizeChannel(opts.Channel)
	region := strings.TrimSpace(opts.Region)
	out := make([]domainsales.SalesOrder, 0, len(orders))
	for _, o := range orders {
		at, ok := orderDate(o)
		if !ok {
			continue
		}
		if checkRange && (at.Before(opts.From) || !at.Before(opts.To)) {
			continue
		}
		if channel != "" && o.ChannelOf() != channel {
			continue
		}
		if region != "" && regionOf(o) != region {
			continue
		}
		out = append(out, o)
	}
	return out
}

// orderDate parses the order timestamp; falls back to the order day.
func orderDate(o domainsales.SalesOrder) (time.Time, bool) {
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02"} {
		v := strings.TrimSpace(o.OrderedAt)
		if v != "" {
			if t, err := time.ParseInLocation(layout, v, Bangkok); err == nil {
				return t, true
			}
		}
	}
	if d := strings.TrimSpace(o.DateDay); d != "" {
		if t, err := time.ParseInLocation("2006-01-02", d, Bangkok); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func regionOf(o domainsales.SalesOrder) string {
	return domainsales.RegionOf(domainsales.NormalizeProvince(o.Province, ""))
}

func buyerID(o domainsales.SalesOrder) string {
	if id := strings.TrimSpace(o.BuyerID); id != "" {
		return id
	}
	return strings.TrimSpace(o.Buyer)
}

// orderUnits is the number of bags/pieces of an order.
func orderUnits(o domainsales.SalesOrder) int64 {
	if len(o.Lines) > 0 {
		var units int64
		for _, l := range o.Lines {
			units += int64(l.Qty)
		}
		return units
	}
	if o.TotalBags > 0 {
		return int64(o.TotalBags)
	}
	return int64(o.ChickenBags + o.TunaBags + o.RabbitBags)
}

func summarize(orders []domainsales.SalesOrder) domainsales.Summary {
	s := domainsales.Summary{Orders: int64(len(orders))}
	buyers := map[string]struct{}{}
	for _, o := range orders {
		s.Sales += o.Amount
		s.Items += orderUnits(o)
		if id := buyerID(o); id != "" {
			buyers[id] = struct{}{}
		}
	}
	s.Buyers = int64(len(buyers))
	s.AOV = ratio(s.Sales, float64(s.Orders))
	s.Sales = round2(s.Sales)
	return s
}

func buildChannels(orders []domainsales.SalesOrder) []domainsales.ChannelPoint {
	type agg struct {
		orders int64
		sales  float64
	}
	byKey := map[string]*agg{}
	for _, o := range orders {
		key := o.ChannelOf()
		if byKey[key] == nil {
			byKey[key] = &agg{}
		}
		byKey[key].orders++
		byKey[key].sales += o.Amount
	}
	var total float64
	for _, a := range byKey {
		total += a.sales
	}
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if byKey[keys[i]].sales != byKey[keys[j]].sales {
			return byKey[keys[i]].sales > byKey[keys[j]].sales
		}
		return keys[i] < keys[j]
	})

	out := make([]domainsales.ChannelPoint, 0, len(keys))
	for _, k := range keys {
		label, ok := domainsales.ChannelLabels[k]
		if !ok {
			label = k
		}
		out = append(out, domainsales.ChannelPoint{
			Channel:      k,
			Label:        label,
			Orders:       byKey[k].orders,
			Sales:        round2(byKey[k].sales),
			SharePercent: percent(byKey[k].sales, total),
		})
	}
	return out
}

type trendBucket struct {
	period                     string
	start, end                 time.Time
	shopeeSales, tiktokSales   float64
	shopeeOrders, tiktokOrders int64
}

// buildTrend groups orders into ISO weeks and calendar months.
func buildTrend(orders []domainsales.SalesOrder, opts Options) domainsales.Trend {
	weeks := map[string]*trendBucket{}
	months := map[string]*trendBucket{}

	for _, o := range orders {
		at, ok := orderDate(o)
		if !ok {
			continue
		}
		addTrend(weeks, isoWeekKey(at), at, o)
		addTrend(months, at.Format("2006-01"), at, o)
	}

	return domainsales.Trend{
		Weekly:  trendSeries(weeks, opts),
		Monthly: trendSeries(months, opts),
	}
}

func addTrend(buckets map[string]*trendBucket, key string, at time.Time, o domainsales.SalesOrder) {
	b := buckets[key]
	if b == nil {
		start, end := periodBounds(key, at)
		b = &trendBucket{period: key, start: start, end: end}
		buckets[key] = b
	}
	if o.Platform == domainsales.PlatformShopee {
		b.shopeeSales += o.Amount
		b.shopeeOrders++
	} else {
		b.tiktokSales += o.Amount
		b.tiktokOrders++
	}
}

func isoWeekKey(at time.Time) string {
	year, week := at.ISOWeek()
	return fmt.Sprintf("%04d-W%02d", year, week)
}

// periodBounds returns the calendar bounds of a week key ("2026-W46") or a
// month key ("2026-11").
func periodBounds(key string, at time.Time) (time.Time, time.Time) {
	if strings.Contains(key, "-W") {
		monday := at.AddDate(0, 0, -((int(at.Weekday()) + 6) % 7))
		monday = time.Date(monday.Year(), monday.Month(), monday.Day(), 0, 0, 0, 0, Bangkok)
		return monday, monday.AddDate(0, 0, 7)
	}
	month, err := time.ParseInLocation("2006-01", key, Bangkok)
	if err != nil {
		return at, at.AddDate(0, 0, 1)
	}
	return month, month.AddDate(0, 1, 0)
}

func trendSeries(buckets map[string]*trendBucket, opts Options) []domainsales.TrendPoint {
	keys := make([]string, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	fromDay := startOfDay(opts.From)
	toDay := startOfDay(opts.To)
	out := make([]domainsales.TrendPoint, 0, len(keys))
	for _, k := range keys {
		b := buckets[k]
		start, end := b.start, b.end
		if start.Before(fromDay) {
			start = fromDay
		}
		if end.After(toDay) {
			end = toDay
		}
		out = append(out, domainsales.TrendPoint{
			Period:       k,
			StartDate:    start.Format("2006-01-02"),
			EndDate:      end.AddDate(0, 0, -1).Format("2006-01-02"),
			ShopeeSales:  round2(b.shopeeSales),
			TikTokSales:  round2(b.tiktokSales),
			TotalSales:   round2(b.shopeeSales + b.tiktokSales),
			ShopeeOrders: b.shopeeOrders,
			TikTokOrders: b.tiktokOrders,
			TotalOrders:  b.shopeeOrders + b.tiktokOrders,
		})
	}
	return out
}

// flavorUnits returns the bag counts of an order per flavor.
func flavorUnits(o domainsales.SalesOrder) map[string]int64 {
	out := map[string]int64{}
	if o.ChickenBags > 0 {
		out[domainsales.FlavorChicken] = int64(o.ChickenBags)
	}
	if o.TunaBags > 0 {
		out[domainsales.FlavorTuna] = int64(o.TunaBags)
	}
	if o.RabbitBags > 0 {
		out[domainsales.FlavorRabbit] = int64(o.RabbitBags)
	}
	if len(out) == 0 {
		for _, l := range o.Lines {
			if l.Flavor == "" || l.Qty <= 0 {
				continue
			}
			out[l.Flavor] += int64(l.Qty)
		}
	}
	return out
}

func hasFlavorData(orders []domainsales.SalesOrder) bool {
	for _, o := range orders {
		if len(flavorUnits(o)) > 0 {
			return true
		}
	}
	return false
}

func buildFlavors(current, previous []domainsales.SalesOrder, opts Options) []domainsales.FlavorRow {
	if !hasFlavorData(current) {
		return nil
	}
	type agg struct {
		units, orders int64
		sales         float64
		months        map[string]*domainsales.MonthValue
	}
	byKey := map[string]*agg{}
	var totalUnits int64

	for _, o := range current {
		units := flavorUnits(o)
		var orderTotal int64
		for _, n := range units {
			orderTotal += n
		}
		at, _ := orderDate(o)
		for key, n := range units {
			a := byKey[key]
			if a == nil {
				a = &agg{months: map[string]*domainsales.MonthValue{}}
				byKey[key] = a
			}
			a.units += n
			a.orders++
			totalUnits += n
			if orderTotal > 0 {
				share := float64(n) / float64(orderTotal)
				a.sales += o.Amount * share
			}
			mk := at.Format("2006-01")
			m := a.months[mk]
			if m == nil {
				m = &domainsales.MonthValue{Month: mk}
				a.months[mk] = m
			}
			m.Units += n
			m.Orders++
			if orderTotal > 0 {
				m.Sales += o.Amount * (float64(n) / float64(orderTotal))
			}
		}
	}

	prevUnits := map[string]int64{}
	for _, o := range previous {
		for key, n := range flavorUnits(o) {
			prevUnits[key] += n
		}
	}

	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if byKey[keys[i]].units != byKey[keys[j]].units {
			return byKey[keys[i]].units > byKey[keys[j]].units
		}
		return keys[i] < keys[j]
	})

	out := make([]domainsales.FlavorRow, 0, len(keys))
	for _, k := range keys {
		a := byKey[k]
		label, ok := domainsales.FlavorLabels[k]
		if !ok {
			label = k
		}
		months := make([]domainsales.MonthValue, 0, len(a.months))
		monthKeys := make([]string, 0, len(a.months))
		for mk := range a.months {
			monthKeys = append(monthKeys, mk)
		}
		sort.Strings(monthKeys)
		var monthTotal float64
		for _, mk := range monthKeys {
			monthTotal += a.months[mk].Sales
		}
		for _, mk := range monthKeys {
			m := *a.months[mk]
			m.Sales = round2(m.Sales)
			m.SharePercent = percent(m.Sales, monthTotal)
			months = append(months, m)
		}

		row := domainsales.FlavorRow{
			Key:          k,
			Label:        label,
			Units:        a.units,
			Orders:       a.orders,
			Sales:        round2(a.sales),
			SharePercent: percent(float64(a.units), float64(totalUnits)),
			Months:       months,
		}
		if p, ok := prevUnits[k]; ok && p > 0 {
			row.ChangePercent = changePercent(float64(a.units), float64(p))
		}
		out = append(out, row)
	}
	return out
}

func buildProducts(orders []domainsales.SalesOrder) []domainsales.ProductRow {
	type agg struct {
		name   string
		units  int64
		orders map[string]struct{}
		sales  float64
	}
	byKey := map[string]*agg{}
	var totalUnits int64

	for _, o := range orders {
		ref := o.OrderNo
		if ref == "" {
			ref = o.Platform + "|" + o.OrderedAt + "|" + buyerID(o)
		}
		if len(o.Lines) > 0 {
			for _, l := range o.Lines {
				key := strings.TrimSpace(l.SKU)
				if key == "" {
					key = strings.TrimSpace(l.Name)
				}
				if key == "" {
					continue
				}
				a := byKey[key]
				if a == nil {
					a = &agg{name: l.Name, orders: map[string]struct{}{}}
					byKey[key] = a
				}
				a.units += int64(l.Qty)
				a.sales += l.Amount
				a.orders[ref] = struct{}{}
				totalUnits += int64(l.Qty)
			}
			continue
		}
		for _, name := range o.Items {
			key := strings.TrimSpace(name)
			if key == "" {
				continue
			}
			a := byKey[key]
			if a == nil {
				a = &agg{name: key, orders: map[string]struct{}{}}
				byKey[key] = a
			}
			a.orders[ref] = struct{}{}
			totalUnits++
			a.units++
		}
	}

	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if byKey[keys[i]].units != byKey[keys[j]].units {
			return byKey[keys[i]].units > byKey[keys[j]].units
		}
		return keys[i] < keys[j]
	})

	out := make([]domainsales.ProductRow, 0, len(keys))
	for _, k := range keys {
		a := byKey[k]
		out = append(out, domainsales.ProductRow{
			SKU:          k,
			Name:         a.name,
			Units:        a.units,
			Orders:       int64(len(a.orders)),
			Sales:        round2(a.sales),
			SharePercent: percent(float64(a.units), float64(totalUnits)),
		})
	}
	return out
}

func buildAddOns(orders []domainsales.SalesOrder) ([]domainsales.SetRow, []domainsales.AccessoryRow) {
	sets := map[string]int64{}
	accessories := map[string]int64{}

	for _, o := range orders {
		for _, name := range o.Sets {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			sets[name]++
		}
		accessories[domainsales.AccessoryCatWand] += int64(o.CatWand)
		accessories[domainsales.AccessoryReplacementHead] += int64(o.ReplacementHead)
		accessories[domainsales.AccessoryCatnip] += int64(o.Catnip)
	}

	setRows := make([]domainsales.SetRow, 0, len(sets))
	for name, count := range sets {
		setRows = append(setRows, domainsales.SetRow{
			Name:         name,
			Orders:       count,
			SharePercent: percent(float64(count), float64(len(orders))),
		})
	}
	sort.Slice(setRows, func(i, j int) bool {
		if setRows[i].Orders != setRows[j].Orders {
			return setRows[i].Orders > setRows[j].Orders
		}
		return setRows[i].Name < setRows[j].Name
	})

	var totalAccessories int64
	for _, n := range accessories {
		totalAccessories += n
	}
	accessoryRows := []domainsales.AccessoryRow{}
	for _, key := range []string{domainsales.AccessoryCatWand, domainsales.AccessoryReplacementHead, domainsales.AccessoryCatnip} {
		label, ok := domainsales.AccessoryLabels[key]
		if !ok {
			label = key
		}
		accessoryRows = append(accessoryRows, domainsales.AccessoryRow{
			Key:          key,
			Label:        label,
			Units:        accessories[key],
			SharePercent: percent(float64(accessories[key]), float64(totalAccessories)),
		})
	}
	return setRows, accessoryRows
}

type provinceAgg struct {
	buyers map[string]struct{}
	orders int64
	sales  float64
}

func buildProvinces(orders []domainsales.SalesOrder) []domainsales.ProvinceRow {
	byProvince := map[string]*provinceAgg{}
	for _, o := range orders {
		name := domainsales.NormalizeProvince(o.Province, "")
		a := byProvince[name]
		if a == nil {
			a = &provinceAgg{buyers: map[string]struct{}{}}
			byProvince[name] = a
		}
		a.orders++
		a.sales += o.Amount
		if id := buyerID(o); id != "" {
			a.buyers[id] = struct{}{}
		}
	}

	var totalSales float64
	for _, a := range byProvince {
		totalSales += a.sales
	}

	rows := make([]domainsales.ProvinceRow, 0, len(byProvince))
	for name, a := range byProvince {
		rows = append(rows, domainsales.ProvinceRow{
			Province:     name,
			Region:       domainsales.RegionOf(name),
			Buyers:       int64(len(a.buyers)),
			Orders:       a.orders,
			Sales:        round2(a.sales),
			AOV:          ratio(a.sales, float64(a.orders)),
			SharePercent: percent(a.sales, totalSales),
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Orders != rows[j].Orders {
			return rows[i].Orders > rows[j].Orders
		}
		return rows[i].Sales > rows[j].Sales
	})
	return rows
}

func buildRegions(provinces []domainsales.ProvinceRow) []domainsales.RegionRow {
	type agg struct {
		provinces map[string]struct{}
		orders    int64
		sales     float64
	}
	byRegion := map[string]*agg{}
	for _, p := range provinces {
		a := byRegion[p.Region]
		if a == nil {
			a = &agg{provinces: map[string]struct{}{}}
			byRegion[p.Region] = a
		}
		if p.Province != domainsales.UnknownProvince {
			a.provinces[p.Province] = struct{}{}
		}
		a.orders += p.Orders
		a.sales += p.Sales
	}

	var totalSales float64
	for _, a := range byRegion {
		totalSales += a.sales
	}

	out := []domainsales.RegionRow{}
	for _, name := range domainsales.Regions() {
		a, ok := byRegion[name]
		if !ok {
			continue
		}
		out = append(out, domainsales.RegionRow{
			Region:        name,
			ProvinceCount: len(a.provinces),
			Orders:        a.orders,
			Sales:         round2(a.sales),
			SharePercent:  percent(a.sales, totalSales),
		})
	}
	if a, ok := byRegion[domainsales.RegionUnknown]; ok {
		out = append(out, domainsales.RegionRow{
			Region:        domainsales.RegionUnknown,
			ProvinceCount: 0,
			Orders:        a.orders,
			Sales:         round2(a.sales),
			SharePercent:  percent(a.sales, totalSales),
		})
	}
	return out
}

type customerAgg struct {
	platform string
	buyer    string
	orders   int64
	sales    float64
	items    int64
	first    string
	last     string
	province string
}

func buildCustomers(orders []domainsales.SalesOrder, opts Options) []domainsales.CustomerRow {
	byKey := map[string]*customerAgg{}
	for _, o := range orders {
		id := buyerID(o)
		if id == "" {
			continue
		}
		if opts.CustomerBuyer != "" && id != opts.CustomerBuyer {
			continue
		}
		if opts.CustomerPlatform != "" && o.Platform != opts.CustomerPlatform {
			continue
		}
		key := o.Platform + "|" + id
		a := byKey[key]
		if a == nil {
			a = &customerAgg{platform: o.Platform, buyer: id}
			byKey[key] = a
		}
		a.orders++
		a.sales += o.Amount
		a.items += orderUnits(o)
		day := o.DateDay
		if at, ok := orderDate(o); ok {
			day = at.Format("2006-01-02")
		}
		if a.first == "" || day < a.first {
			a.first = day
		}
		if day > a.last {
			a.last = day
		}
		if name := domainsales.NormalizeProvince(o.Province, ""); name != domainsales.UnknownProvince {
			a.province = name
		}
	}

	rows := make([]domainsales.CustomerRow, 0, len(byKey))
	for _, a := range byKey {
		buyer := a.buyer
		if opts.mask {
			buyer = maskBuyer(buyer)
		}
		rows = append(rows, domainsales.CustomerRow{
			Platform:        a.platform,
			Buyer:           buyer,
			Orders:          a.orders,
			Sales:           round2(a.sales),
			AOV:             ratio(a.sales, float64(a.orders)),
			Items:           a.items,
			FirstOrder:      a.first,
			LastOrder:       a.last,
			Province:        a.province,
			FrequencyBucket: frequencyLabel(a.orders),
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Sales != rows[j].Sales {
			return rows[i].Sales > rows[j].Sales
		}
		return rows[i].Orders > rows[j].Orders
	})
	if len(rows) > opts.MaxCustomers {
		rows = rows[:opts.MaxCustomers]
	}
	return rows
}

func frequencyLabel(orders int64) string {
	switch {
	case orders >= 7:
		return "7 ครั้งขึ้นไป"
	case orders >= 4:
		return "4–6 ครั้ง"
	case orders >= 2:
		return "2–3 ครั้ง"
	default:
		return "1 ครั้ง"
	}
}

func buildFrequencies(orders []domainsales.SalesOrder) []domainsales.CustomerFrequency {
	counts := map[string]int64{}
	for _, o := range orders {
		id := buyerID(o)
		if id == "" {
			continue
		}
		counts[o.Platform+"|"+id]++
	}
	buckets := []string{"1 ครั้ง", "2–3 ครั้ง", "4–6 ครั้ง", "7 ครั้งขึ้นไป"}
	byBucket := map[string]int64{}
	for _, n := range counts {
		byBucket[frequencyLabel(n)]++
	}
	out := make([]domainsales.CustomerFrequency, 0, len(buckets))
	for _, label := range buckets {
		out = append(out, domainsales.CustomerFrequency{
			Label:        label,
			Buyers:       byBucket[label],
			SharePercent: percent(float64(byBucket[label]), float64(len(counts))),
		})
	}
	return out
}

func buildTiming(orders []domainsales.SalesOrder) domainsales.Timing {
	hours := make([]domainsales.HourRow, 24)
	for i := range hours {
		hours[i].Hour = i
	}
	weekdays := make([]domainsales.WeekdayRow, 7)
	for i := range weekdays {
		weekdays[i].Day = i + 1
		weekdays[i].Label = weekdayLabel(i + 1)
	}
	days := make([]domainsales.DayOfMonthRow, 31)
	for i := range days {
		days[i].Day = i + 1
	}

	for _, o := range orders {
		at, ok := orderDate(o)
		if !ok {
			continue
		}
		if o.HasTime {
			hours[at.Hour()].Orders++
			hours[at.Hour()].Sales += o.Amount
		}
		wd := int(at.Weekday())
		if wd == 0 {
			wd = 7
		}
		weekdays[wd-1].Orders++
		weekdays[wd-1].Sales += o.Amount
		days[at.Day()-1].Orders++
		days[at.Day()-1].Sales += o.Amount
	}

	timing := domainsales.Timing{Hours: hours, Weekdays: weekdays, DaysOfMonth: days}
	var peakOrders int64
	for _, h := range hours {
		if h.Orders > peakOrders {
			peakOrders = h.Orders
			timing.PeakHour = h.Hour
		}
	}
	peakOrders = 0
	for _, w := range weekdays {
		if w.Orders > peakOrders {
			peakOrders = w.Orders
			timing.PeakWeekday = w.Label
		}
	}
	peakOrders = 0
	for _, d := range days {
		if d.Orders > peakOrders {
			peakOrders = d.Orders
			timing.PeakDay = d.Day
		}
	}
	for i := range timing.Hours {
		timing.Hours[i].Sales = round2(timing.Hours[i].Sales)
	}
	for i := range timing.Weekdays {
		timing.Weekdays[i].Sales = round2(timing.Weekdays[i].Sales)
	}
	for i := range timing.DaysOfMonth {
		timing.DaysOfMonth[i].Sales = round2(timing.DaysOfMonth[i].Sales)
	}
	return timing
}

func weekdayLabel(day int) string {
	switch day {
	case 1:
		return "จันทร์"
	case 2:
		return "อังคาร"
	case 3:
		return "พุธ"
	case 4:
		return "พฤหัสบดี"
	case 5:
		return "ศุกร์"
	case 6:
		return "เสาร์"
	default:
		return "อาทิตย์"
	}
}

// repurchaseBuckets are the gap buckets of the repurchase report.
var repurchaseBuckets = []struct {
	label    string
	min, max int
}{
	{"1–7 วัน", 1, 7},
	{"8–30 วัน", 8, 30},
	{"31–60 วัน", 31, 60},
	{"61–90 วัน", 61, 90},
	{"91 วันขึ้นไป", 91, math.MaxInt32},
}

func buildRepurchase(orders []domainsales.SalesOrder) domainsales.Repurchase {
	datesByBuyer := map[string][]time.Time{}
	for _, o := range orders {
		id := buyerID(o)
		if id == "" {
			continue
		}
		at, ok := orderDate(o)
		if !ok {
			continue
		}
		key := o.Platform + "|" + id
		datesByBuyer[key] = append(datesByBuyer[key], startOfDay(at))
	}

	counts := make([]int64, len(repurchaseBuckets))
	var repeatBuyers int64
	var gaps int64
	var gapDaysTotal float64

	for _, dates := range datesByBuyer {
		if len(dates) < 2 {
			continue
		}
		repeatBuyers++
		sort.Slice(dates, func(i, j int) bool { return dates[i].Before(dates[j]) })
		for i := 1; i < len(dates); i++ {
			days := int(dates[i].Sub(dates[i-1]).Hours() / 24)
			if days < 1 {
				days = 1
			}
			gaps++
			gapDaysTotal += float64(days)
			for b, bucket := range repurchaseBuckets {
				if days >= bucket.min && days <= bucket.max {
					counts[b]++
					break
				}
			}
		}
	}

	out := domainsales.Repurchase{
		RepeatBuyers: repeatBuyers,
	}
	out.Buckets = make([]domainsales.RepurchaseBucket, 0, len(repurchaseBuckets))
	for i, bucket := range repurchaseBuckets {
		out.Buckets = append(out.Buckets, domainsales.RepurchaseBucket{
			Label:        bucket.label,
			MinDays:      bucket.min,
			MaxDays:      bucket.max,
			Buyers:       counts[i],
			SharePercent: percent(float64(counts[i]), float64(gaps)),
		})
	}
	if buyers := int64(len(datesByBuyer)); buyers > 0 {
		out.RepeatRatePercent = percent(float64(repeatBuyers), float64(buyers))
	}
	if gaps > 0 {
		out.AverageGapDays = round2(gapDaysTotal / float64(gaps))
	}
	return out
}

// CustomerOrder is one order of a buyer drill-down.
type CustomerOrder struct {
	Platform     string
	OrderNo      string
	Date         string
	Amount       float64
	Channel      string
	ChannelLabel string
	Province     string
	Units        int64
}

// CustomerDetail is one buyer with their orders, newest first.
type CustomerDetail struct {
	Customer domainsales.CustomerRow
	Orders   []CustomerOrder
}

// Detail returns the drill-down of one platform buyer. unmasked keeps the
// username as-is; the caller decides based on the actor's permissions.
func Detail(orders []domainsales.SalesOrder, platform, buyer string, unmasked bool) (CustomerDetail, error) {
	if strings.TrimSpace(buyer) == "" {
		return CustomerDetail{}, domain.NewValidationError("buyer", "is required")
	}
	matching := make([]domainsales.SalesOrder, 0, len(orders))
	for _, o := range orders {
		if o.Platform != platform || buyerID(o) != buyer {
			continue
		}
		matching = append(matching, o)
	}
	if len(matching) == 0 {
		return CustomerDetail{}, domain.ErrNotFound
	}

	opts := Options{Unmasked: unmasked, CustomerPlatform: platform, CustomerBuyer: buyer, MaxCustomers: 1}
	// Customer rows need a period for the filter; use the matching orders' own
	// span so the row covers every fetched order of the buyer.
	rows := buildCustomers(matching, opts)
	if len(rows) == 0 {
		return CustomerDetail{}, domain.ErrNotFound
	}

	detail := CustomerDetail{Customer: rows[0]}
	for _, o := range matching {
		day := o.DateDay
		if at, ok := orderDate(o); ok {
			day = at.Format("2006-01-02")
		}
		detail.Orders = append(detail.Orders, CustomerOrder{
			Platform:     o.Platform,
			OrderNo:      o.OrderNo,
			Date:         day,
			Amount:       o.Amount,
			Channel:      o.ChannelOf(),
			ChannelLabel: o.ChannelLabel(),
			Province:     domainsales.NormalizeProvince(o.Province, ""),
			Units:        orderUnits(o),
		})
	}
	sort.Slice(detail.Orders, func(i, j int) bool {
		if detail.Orders[i].Date != detail.Orders[j].Date {
			return detail.Orders[i].Date > detail.Orders[j].Date
		}
		return detail.Orders[i].OrderNo > detail.Orders[j].OrderNo
	})
	return detail, nil
}

// MaskBuyer hides most of a marketplace username ("al***34") for exports.
func MaskBuyer(name string) string { return maskBuyer(name) }

func maskBuyer(name string) string {
	runes := []rune(strings.TrimSpace(name))
	if len(runes) <= 4 {
		return "***"
	}
	return string(runes[:2]) + "***" + string(runes[len(runes)-2:])
}

func startOfDay(t time.Time) time.Time {
	in := t.In(Bangkok)
	return time.Date(in.Year(), in.Month(), in.Day(), 0, 0, 0, 0, Bangkok)
}

func ratio(total, count float64) float64 {
	if count == 0 {
		return 0
	}
	return round2(total / count)
}

func percent(part, total float64) float64 {
	if total == 0 {
		return 0
	}
	return round2(part / total * 100)
}

// changePercent is the percentage change from prev to cur; nil when prev is 0.
func changePercent(cur, prev float64) *float64 {
	if prev == 0 {
		return nil
	}
	v := round2((cur - prev) / prev * 100)
	return &v
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}

// nonNil returns an empty slice instead of nil so JSON encodes [] not null.
func nonNil(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

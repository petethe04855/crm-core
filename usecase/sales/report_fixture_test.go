// Test fixture: reads an exported dashboard report (customer_sales_analysis.html)
// into order facts so reconcile_test.go can check the dashboard formulas
// against the figures the report precomputed. Not used in production: the
// dashboard reads orders from the ERP.
package sales

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"crm-core/domain"
	domainsales "crm-core/domain/sales"
)

// Import limits.
const (
	// MaxImportBytes bounds the uploaded report (the reference report is ~7 MB).
	MaxImportBytes = 20 << 20
	// MaxImportOrders bounds rows per snapshot.
	MaxImportOrders = 200000
)

// analyticsScript finds the opening tag of the embedded report data.
var analyticsScript = regexp.MustCompile(`(?i)<script[^>]*\bid\s*=\s*["']analytics-data["'][^>]*>`)

// flexString accepts a JSON string or number (order ids are sometimes numeric).
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		*f = ""
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = flexString(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*f = flexString(n.String())
	return nil
}

// rawOrder lists only the order fields the CRM keeps. Anything else in the
// report, including customers[].recipients (names), is never decoded.
type rawOrder struct {
	ID             flexString `json:"id"`
	Date           string     `json:"date"`
	DateDay        string     `json:"date_day"`
	Amount         float64    `json:"amount"`
	Buyer          string     `json:"buyer"`
	Platform       string     `json:"platform"`
	Channel        string     `json:"channel"`
	ChannelType    string     `json:"channel_type"`
	Province       string     `json:"province"`
	Sets           []string   `json:"sets"`
	Items          []string   `json:"items"`
	ChickenBags    int        `json:"chicken_bags"`
	TunaBags       int        `json:"tuna_bags"`
	RabbitBags     int        `json:"rabbit_bags"`
	TotalBags      int        `json:"total_bags"`
	AccessoryUnits struct {
		CatWand         int `json:"cat_wand"`
		ReplacementHead int `json:"replacement_head"`
		Catnip          int `json:"catnip"`
	} `json:"accessory_units"`
}

type rawReport struct {
	AllOrders *[]rawOrder `json:"all_orders"`
}

var validChannelTypes = map[string]string{
	domainsales.ChannelShopee:        domainsales.PlatformShopee,
	domainsales.ChannelTikTokLive:    domainsales.PlatformTikTok,
	domainsales.ChannelTikTokProduct: domainsales.PlatformTikTok,
	domainsales.ChannelTikTokVideo:   domainsales.PlatformTikTok,
}

// extractReportJSON returns the JSON document inside an exported dashboard
// HTML file, or the input itself when it already is JSON.
func extractReportJSON(data []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")))
	if len(trimmed) > 0 && trimmed[0] == '{' {
		return trimmed, nil
	}
	loc := analyticsScript.FindIndex(data)
	if loc == nil {
		return nil, domain.NewValidationError("file", "ไม่พบข้อมูลรายงาน (analytics-data) ในไฟล์ กรุณาใช้ไฟล์ customer_sales_analysis.html")
	}
	rest := data[loc[1]:]
	end := bytes.Index(bytes.ToLower(rest), []byte("</script>"))
	if end < 0 {
		return nil, domain.NewValidationError("file", "ไฟล์รายงานไม่สมบูรณ์")
	}
	return bytes.TrimSpace(rest[:end]), nil
}

// ParseReport turns an exported report (HTML or JSON) into order facts. It
// validates every row and keeps no personal data beyond the masked buyer id.
func ParseReport(data []byte) ([]domainsales.SalesOrder, error) {
	if len(data) == 0 {
		return nil, domain.NewValidationError("file", "ไฟล์ว่าง")
	}
	if len(data) > MaxImportBytes {
		return nil, domain.NewValidationError("file", fmt.Sprintf("ไฟล์ใหญ่เกิน %d MB", MaxImportBytes>>20))
	}
	doc, err := extractReportJSON(data)
	if err != nil {
		return nil, err
	}
	var report rawReport
	if err := json.Unmarshal(doc, &report); err != nil {
		return nil, domain.NewValidationError("file", "อ่านข้อมูลรายงานไม่ได้ (JSON ไม่ถูกต้อง)")
	}
	if report.AllOrders == nil {
		return nil, domain.NewValidationError("file", "รายงานไม่มีรายการคำสั่งซื้อ (all_orders)")
	}
	rows := *report.AllOrders
	if len(rows) == 0 {
		return nil, domain.NewValidationError("file", "รายงานไม่มีคำสั่งซื้อ")
	}
	if len(rows) > MaxImportOrders {
		return nil, domain.NewValidationError("file", fmt.Sprintf("คำสั่งซื้อเกิน %d รายการ", MaxImportOrders))
	}

	seen := make(map[string]struct{}, len(rows))
	out := make([]domainsales.SalesOrder, 0, len(rows))
	for i, r := range rows {
		o, err := toOrder(r)
		if err != nil {
			return nil, domain.NewValidationError("file", fmt.Sprintf("คำสั่งซื้อแถวที่ %d: %s", i+1, err.Error()))
		}
		key := o.Platform + "|" + o.OrderNo
		if _, dup := seen[key]; dup {
			return nil, domain.NewValidationError("file", fmt.Sprintf("เลขคำสั่งซื้อ %s ซ้ำ", o.OrderNo))
		}
		seen[key] = struct{}{}
		out = append(out, o)
	}
	return out, nil
}

func toOrder(r rawOrder) (domainsales.SalesOrder, error) {
	id := strings.TrimSpace(string(r.ID))
	if id == "" || len(id) > 64 {
		return domainsales.SalesOrder{}, fmt.Errorf("เลขคำสั่งซื้อไม่ถูกต้อง")
	}
	at, err := time.ParseInLocation("2006-01-02 15:04:05", strings.TrimSpace(r.Date), Bangkok)
	if err != nil {
		if at, err = time.ParseInLocation("2006-01-02", strings.TrimSpace(r.Date), Bangkok); err != nil {
			return domainsales.SalesOrder{}, fmt.Errorf("วันที่ %q ไม่ถูกต้อง", r.Date)
		}
	}
	day := at.Format("2006-01-02")
	if r.Amount < 0 || r.Amount > 10_000_000 {
		return domainsales.SalesOrder{}, fmt.Errorf("ยอดเงินไม่ถูกต้อง")
	}
	platform, ok := validChannelTypes[r.ChannelType]
	if !ok {
		return domainsales.SalesOrder{}, fmt.Errorf("ช่องทาง %q ไม่รู้จัก", r.ChannelType)
	}
	if r.Platform != platform {
		return domainsales.SalesOrder{}, fmt.Errorf("แพลตฟอร์ม %q ไม่ตรงกับช่องทาง %q", r.Platform, r.ChannelType)
	}
	buyer := strings.TrimSpace(r.Buyer)
	if buyer == "" || utf8.RuneCountInString(buyer) > 128 {
		return domainsales.SalesOrder{}, fmt.Errorf("ผู้ซื้อไม่ถูกต้อง")
	}
	for _, n := range []int{r.ChickenBags, r.TunaBags, r.RabbitBags, r.TotalBags,
		r.AccessoryUnits.CatWand, r.AccessoryUnits.ReplacementHead, r.AccessoryUnits.Catnip} {
		if n < 0 {
			return domainsales.SalesOrder{}, fmt.Errorf("จำนวนสินค้าติดลบ")
		}
	}
	total := r.TotalBags
	if total == 0 {
		total = r.ChickenBags + r.TunaBags + r.RabbitBags
	}
	return domainsales.SalesOrder{
		Platform:        platform,
		OrderNo:         id,
		OrderedAt:       at.Format("2006-01-02 15:04:05"),
		HasTime:         true,
		BuyerID:         buyer,
		DateDay:         day,
		Amount:          round2(r.Amount),
		Buyer:           buyer,
		Channel:         truncate(strings.TrimSpace(r.Channel), 40),
		ChannelType:     r.ChannelType,
		Province:        truncate(strings.TrimSpace(r.Province), 120),
		Sets:            nonNil(r.Sets),
		Items:           nonNil(r.Items),
		ChickenBags:     r.ChickenBags,
		TunaBags:        r.TunaBags,
		RabbitBags:      r.RabbitBags,
		TotalBags:       total,
		CatWand:         r.AccessoryUnits.CatWand,
		ReplacementHead: r.AccessoryUnits.ReplacementHead,
		Catnip:          r.AccessoryUnits.Catnip,
	}, nil
}

func truncate(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max])
}

package coupon

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"crm-core/domain"
	domaincoupon "crm-core/domain/coupon"
)

var sharedCodePattern = regexp.MustCompile(`^[A-Z0-9-]{3,40}$`)

func validStatus(s string) bool {
	switch s {
	case domaincoupon.StatusDraft, domaincoupon.StatusActive, domaincoupon.StatusPaused, domaincoupon.StatusEnded:
		return true
	}
	return false
}

func validHolderStatus(s string) bool {
	switch s {
	case domaincoupon.HolderAvailable, domaincoupon.HolderUsed, domaincoupon.HolderExpired, domaincoupon.HolderCancelled:
		return true
	}
	return false
}

func validChannel(s string) bool {
	switch s {
	case domaincoupon.ChannelStore, domaincoupon.ChannelLine, domaincoupon.ChannelShopee, domaincoupon.ChannelTikTok:
		return true
	}
	return false
}

func normalizeInput(in Input) Input {
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	in.Terms = strings.TrimSpace(in.Terms)
	in.ImageURL = strings.TrimSpace(in.ImageURL)
	in.BenefitType = strings.TrimSpace(in.BenefitType)
	in.ExpiryMode = strings.TrimSpace(in.ExpiryMode)
	in.Channel = strings.TrimSpace(in.Channel)
	in.CodeType = strings.TrimSpace(in.CodeType)
	in.SharedCode = strings.ToUpper(strings.TrimSpace(in.SharedCode))
	if in.LimitPerMember == 0 {
		in.LimitPerMember = 1
	}
	if in.CodeType == domaincoupon.CodeUnique {
		in.SharedCode = ""
	}
	switch in.BenefitType {
	case domaincoupon.BenefitFreeShipping, domaincoupon.BenefitFreeItem:
		in.BenefitValue, in.MaxDiscount = 0, nil
	case domaincoupon.BenefitAmountOff:
		in.MaxDiscount = nil
	}
	if in.ExpiryMode == domaincoupon.ExpiryFixedDate {
		in.ValidDays = 0
	}
	return in
}

// validate checks the template on its own. readyToActivate adds the checks that
// only matter once members can claim it.
func (u *usecase) validate(ctx context.Context, in Input) error {
	if n := utf8.RuneCountInString(in.Name); n < 1 || n > domaincoupon.MaxNameRunes {
		return domain.NewValidationError("name", "is required (120 characters max)")
	}
	if utf8.RuneCountInString(in.Description) > domaincoupon.MaxTextRunes {
		return domain.NewValidationError("description", "must be 2000 characters or fewer")
	}
	if utf8.RuneCountInString(in.Terms) > domaincoupon.MaxTextRunes {
		return domain.NewValidationError("terms", "must be 2000 characters or fewer")
	}
	if in.ImageURL != "" && !strings.HasPrefix(in.ImageURL, "https://") && !strings.HasPrefix(in.ImageURL, "/") {
		return domain.NewValidationError("imageUrl", "must be an https:// URL or an uploaded file")
	}

	switch in.BenefitType {
	case domaincoupon.BenefitAmountOff:
		if in.BenefitValue <= 0 {
			return domain.NewValidationError("benefitValue", "must be greater than zero")
		}
	case domaincoupon.BenefitPercentOff:
		if in.BenefitValue <= 0 || in.BenefitValue > 100 {
			return domain.NewValidationError("benefitValue", "must be between 0 and 100 percent")
		}
		if in.MaxDiscount == nil || *in.MaxDiscount <= 0 {
			return domain.NewValidationError("maxDiscount", "is required for a percent discount")
		}
	case domaincoupon.BenefitFreeShipping, domaincoupon.BenefitFreeItem:
	default:
		return domain.NewValidationError("benefitType", "must be one of amount_off, percent_off, free_shipping, free_item")
	}
	if in.MinSpend < 0 {
		return domain.NewValidationError("minSpend", "must not be negative")
	}
	if in.PointsCost < 0 || in.PointsCost > domaincoupon.MaxPointsCost {
		return domain.NewValidationError("pointsCost", "must be between 0 and 10,000,000")
	}

	if in.ClaimStartAt != nil && in.ClaimEndAt != nil && !in.ClaimStartAt.Before(*in.ClaimEndAt) {
		return domain.NewValidationError("claimEndAt", "must be after claimStartAt")
	}
	switch in.ExpiryMode {
	case domaincoupon.ExpiryFixedDate:
		if in.ExpiresAt == nil {
			return domain.NewValidationError("expiresAt", "is required for a fixed expiry date")
		}
		if in.ClaimEndAt != nil && in.ExpiresAt.Before(*in.ClaimEndAt) {
			return domain.NewValidationError("expiresAt", "must not be before the end of the claim window")
		}
	case domaincoupon.ExpiryDaysAfterClaim:
		if in.ValidDays < 1 || in.ValidDays > domaincoupon.MaxValidDays {
			return domain.NewValidationError("validDays", "must be between 1 and 365")
		}
	default:
		return domain.NewValidationError("expiryMode", "must be fixed_date or days_after_claim")
	}

	if in.TotalQuota < 0 || in.TotalQuota > domaincoupon.MaxQuota {
		return domain.NewValidationError("totalQuota", "must be between 0 (unlimited) and 1,000,000")
	}
	if in.LimitPerMember < 1 || in.LimitPerMember > 100 {
		return domain.NewValidationError("limitPerMember", "must be between 1 and 100")
	}

	if !validChannel(in.Channel) {
		return domain.NewValidationError("channel", "must be one of store, line, shopee, tiktok")
	}
	switch in.CodeType {
	case domaincoupon.CodeUnique:
		if in.Channel == domaincoupon.ChannelShopee || in.Channel == domaincoupon.ChannelTikTok {
			return domain.NewValidationError("codeType", "marketplace coupons must use a shared shop code")
		}
	case domaincoupon.CodeShared:
		if !sharedCodePattern.MatchString(in.SharedCode) {
			return domain.NewValidationError("sharedCode", "must be 3–40 characters of A–Z, 0–9 or -")
		}
	default:
		return domain.NewValidationError("codeType", "must be unique or shared")
	}

	if in.MinTierID != nil && u.tiers != nil {
		if _, err := u.tiers.FindByID(ctx, *in.MinTierID); err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return domain.NewValidationError("minTierId", "tier does not exist")
			}
			return err
		}
	}
	return nil
}

func readyToActivate(in Input, now time.Time) error {
	if in.ExpiryMode == domaincoupon.ExpiryFixedDate && in.ExpiresAt != nil && !in.ExpiresAt.After(now) {
		return domain.NewValidationError("expiresAt", "must be in the future to activate")
	}
	if in.ClaimEndAt != nil && !in.ClaimEndAt.After(now) {
		return domain.NewValidationError("claimEndAt", "must be in the future to activate")
	}
	return nil
}

func apply(c *domaincoupon.Coupon, in Input) {
	c.Name, c.Description, c.ImageURL, c.Terms = in.Name, in.Description, in.ImageURL, in.Terms
	c.BenefitType, c.BenefitValue, c.MaxDiscount, c.MinSpend = in.BenefitType, in.BenefitValue, in.MaxDiscount, in.MinSpend
	c.PointsCost = in.PointsCost
	c.ClaimStartAt, c.ClaimEndAt = in.ClaimStartAt, in.ClaimEndAt
	c.ExpiryMode, c.ExpiresAt, c.ValidDays = in.ExpiryMode, in.ExpiresAt, in.ValidDays
	c.TotalQuota, c.LimitPerMember, c.MinTierID = in.TotalQuota, in.LimitPerMember, in.MinTierID
	c.Channel, c.CodeType, c.SharedCode = in.Channel, in.CodeType, in.SharedCode
}

func inputOf(c domaincoupon.Coupon) Input {
	return Input{
		Name: c.Name, Description: c.Description, ImageURL: c.ImageURL, Terms: c.Terms,
		BenefitType: c.BenefitType, BenefitValue: c.BenefitValue, MaxDiscount: c.MaxDiscount, MinSpend: c.MinSpend,
		PointsCost: c.PointsCost, ClaimStartAt: c.ClaimStartAt, ClaimEndAt: c.ClaimEndAt,
		ExpiryMode: c.ExpiryMode, ExpiresAt: c.ExpiresAt, ValidDays: c.ValidDays,
		TotalQuota: c.TotalQuota, LimitPerMember: c.LimitPerMember, MinTierID: c.MinTierID,
		Channel: c.Channel, CodeType: c.CodeType, SharedCode: c.SharedCode,
	}
}

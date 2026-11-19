package member

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"crm-core/domain"
	domainmember "crm-core/domain/member"
)

const (
	maxTagRunes  = 40
	maxNoteRunes = 2000
)

// statusChanges are the transitions the backoffice may make. Soft deletion is
// not offered in the UI; deleted members stay readable for audit.
var statusChanges = map[string]bool{
	domainmember.StatusActive:    true,
	domainmember.StatusSuspended: true,
}

// SetStatus suspends or reactivates a member.
func (u *usecase) SetStatus(ctx context.Context, actor domain.Actor, id uint, status string) (*domainmember.Member, error) {
	if id == 0 {
		return nil, domain.NewValidationError("id", "is required")
	}
	status = strings.ToLower(strings.TrimSpace(status))
	if !statusChanges[status] {
		return nil, domain.NewValidationError("status", "must be active or suspended")
	}
	if _, err := u.members.FindByID(ctx, id); err != nil {
		return nil, err
	}
	if err := u.members.UpdateStatus(ctx, id, status); err != nil {
		return nil, err
	}
	return u.members.FindByID(ctx, id)
}

// UnlinkLine detaches the member's active LINE account. The member keeps their
// points and history and can link a new account later.
func (u *usecase) UnlinkLine(ctx context.Context, actor domain.Actor, id uint) error {
	if id == 0 {
		return domain.NewValidationError("id", "is required")
	}
	if _, err := u.members.FindByID(ctx, id); err != nil {
		return err
	}
	return u.members.UnlinkActiveLineAccount(ctx, id)
}

// Tags lists a member's backoffice tags.
func (u *usecase) Tags(ctx context.Context, id uint) ([]domainmember.MemberTag, error) {
	if id == 0 {
		return nil, domain.NewValidationError("id", "is required")
	}
	if _, err := u.members.FindByID(ctx, id); err != nil {
		return nil, err
	}
	return u.members.FindTags(ctx, id)
}

// AddTag normalises and stores a tag. Tags are lowercased so "VIP" and "vip"
// cannot both exist on the same member.
func (u *usecase) AddTag(ctx context.Context, actor domain.Actor, id uint, raw string) ([]domainmember.MemberTag, error) {
	if id == 0 {
		return nil, domain.NewValidationError("id", "is required")
	}
	tag, err := NormalizeTag(raw)
	if err != nil {
		return nil, err
	}
	if _, err := u.members.FindByID(ctx, id); err != nil {
		return nil, err
	}
	if err := u.members.AddTag(ctx, &domainmember.MemberTag{MemberID: id, Tag: tag, CreatedBy: actor.ERPUserID}); err != nil {
		return nil, err
	}
	return u.members.FindTags(ctx, id)
}

// RemoveTag deletes a tag from the member.
func (u *usecase) RemoveTag(ctx context.Context, actor domain.Actor, id uint, raw string) ([]domainmember.MemberTag, error) {
	if id == 0 {
		return nil, domain.NewValidationError("id", "is required")
	}
	tag, err := NormalizeTag(raw)
	if err != nil {
		return nil, err
	}
	if err := u.members.RemoveTag(ctx, id, tag); err != nil {
		return nil, err
	}
	return u.members.FindTags(ctx, id)
}

// Notes lists the internal notes of a member, newest first.
func (u *usecase) Notes(ctx context.Context, id uint, page, limit int) ([]domainmember.MemberNote, int64, error) {
	if id == 0 {
		return nil, 0, domain.NewValidationError("id", "is required")
	}
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	if _, err := u.members.FindByID(ctx, id); err != nil {
		return nil, 0, err
	}
	return u.members.FindNotes(ctx, id, page, limit)
}

// AddNote appends an internal note.
func (u *usecase) AddNote(ctx context.Context, actor domain.Actor, id uint, body string) (*domainmember.MemberNote, error) {
	if id == 0 {
		return nil, domain.NewValidationError("id", "is required")
	}
	body = strings.TrimSpace(body)
	switch n := utf8.RuneCountInString(body); {
	case n == 0:
		return nil, domain.NewValidationError("body", "is required")
	case n > maxNoteRunes:
		return nil, domain.NewValidationError("body", "must be 2000 characters or fewer")
	}
	if _, err := u.members.FindByID(ctx, id); err != nil {
		return nil, err
	}
	note := &domainmember.MemberNote{
		MemberID:       id,
		Body:           body,
		CreatedBy:      actor.ERPUserID,
		CreatedByEmail: actor.Email,
		CreatedAt:      time.Now(),
	}
	if err := u.members.AddNote(ctx, note); err != nil {
		return nil, err
	}
	return note, nil
}

// NormalizeTag trims, lowercases and validates a tag.
func NormalizeTag(raw string) (string, error) {
	tag := strings.ToLower(strings.Join(strings.Fields(raw), " "))
	if tag == "" {
		return "", domain.NewValidationError("tag", "is required")
	}
	if utf8.RuneCountInString(tag) > maxTagRunes {
		return "", domain.NewValidationError("tag", "must be 40 characters or fewer")
	}
	return tag, nil
}

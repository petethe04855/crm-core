package points

import (
	"context"
	"time"

	"crm-core/domain"
	domainearnrule "crm-core/domain/earnrule"
	domainmember "crm-core/domain/member"
)

// fakeRules is an in-memory earn-rule repository. FindActive returns whatever
// the test put in active, so the test controls the window filter; lastAt
// records the instant the calculator asked about.
type fakeRules struct {
	active []domainearnrule.EarnRule
	all    []domainearnrule.EarnRule
	byID   map[uint]*domainearnrule.EarnRule
	lastAt time.Time
	lastQ  domainearnrule.Query
	create func(rule *domainearnrule.EarnRule)
	update func(rule *domainearnrule.EarnRule)
	err    error
}

func (f *fakeRules) FindAll(_ context.Context, q domainearnrule.Query) ([]domainearnrule.EarnRule, error) {
	f.lastQ = q
	return f.all, f.err
}

func (f *fakeRules) FindByID(_ context.Context, id uint) (*domainearnrule.EarnRule, error) {
	if rule, ok := f.byID[id]; ok {
		return rule, nil
	}
	return nil, domain.ErrNotFound
}

func (f *fakeRules) FindActive(_ context.Context, at time.Time) ([]domainearnrule.EarnRule, error) {
	f.lastAt = at
	return f.active, f.err
}

func (f *fakeRules) Create(_ context.Context, rule *domainearnrule.EarnRule) error {
	if f.create != nil {
		f.create(rule)
	}
	return f.err
}

func (f *fakeRules) Update(_ context.Context, rule *domainearnrule.EarnRule) error {
	if f.update != nil {
		f.update(rule)
	}
	return f.err
}

// fakeMembers returns one fixed member; the guard and Earn only read it.
type fakeMembers struct {
	member *domainmember.Member
	err    error
}

func (f *fakeMembers) FindByID(_ context.Context, _ uint) (*domainmember.Member, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.member, nil
}

func (f *fakeMembers) FindAll(context.Context, domainmember.Query) ([]domainmember.Member, int64, error) {
	return nil, 0, nil
}
func (f *fakeMembers) FindActiveLineAccount(context.Context, uint) (*domainmember.MemberLineAccount, error) {
	return nil, domain.ErrNotFound
}
func (f *fakeMembers) UpdateStatus(context.Context, uint, string) error { return nil }
func (f *fakeMembers) UnlinkActiveLineAccount(context.Context, uint) error {
	return nil
}
func (f *fakeMembers) FindTags(context.Context, uint) ([]domainmember.MemberTag, error) {
	return nil, nil
}
func (f *fakeMembers) AddTag(context.Context, *domainmember.MemberTag) error { return nil }
func (f *fakeMembers) RemoveTag(context.Context, uint, string) error         { return nil }
func (f *fakeMembers) FindNotes(context.Context, uint, int, int) ([]domainmember.MemberNote, int64, error) {
	return nil, 0, nil
}
func (f *fakeMembers) AddNote(context.Context, *domainmember.MemberNote) error { return nil }

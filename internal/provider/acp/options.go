package acp

import (
	"context"
	"fmt"
	"strconv"

	sdk "github.com/coder/acp-go-sdk"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
)

// ACP has two ways for an agent to offer session settings. Config options are the
// general one: any number of selects and on/off switches, each with a category hint.
// Session modes are the older one: a single list of modes. An agent that sends config
// options is taken at its word, and its modes are then ignored, as the protocol says
// the options cover them.

// modeOptionID is the id inaba gives the one option made from session modes.
const modeOptionID = "mode"

func (s *session) setup(options []sdk.SessionConfigOption, modes *sdk.SessionModeState) {
	if len(options) > 0 {
		s.reportConfig(options)
		return
	}
	if modes != nil && len(modes.AvailableModes) > 0 {
		s.mu.Lock()
		s.modes = modes
		s.mu.Unlock()
		s.reportMode(string(modes.CurrentModeId))
	}
}

func text(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func (s *session) reportConfig(options []sdk.SessionConfigOption) {
	out := make([]*inabav1.SessionOption, 0, len(options))
	booleans := map[string]bool{}
	for _, o := range options {
		switch {
		case o.Boolean != nil:
			b := o.Boolean
			booleans[string(b.Id)] = true
			out = append(out, &inabav1.SessionOption{Id: string(b.Id), Name: b.Name, Description: text(b.Description), Category: category(b.Category), Boolean: true, Value: strconv.FormatBool(b.CurrentValue)})
		case o.Select != nil:
			sel := o.Select
			opt := &inabav1.SessionOption{Id: string(sel.Id), Name: sel.Name, Description: text(sel.Description), Category: category(sel.Category), Value: string(sel.CurrentValue)}
			if flat := sel.Options.Ungrouped; flat != nil {
				for _, c := range *flat {
					opt.Choices = append(opt.Choices, &inabav1.SessionOption_Choice{Value: string(c.Value), Name: c.Name, Description: text(c.Description)})
				}
			}
			if groups := sel.Options.Grouped; groups != nil {
				for _, g := range *groups {
					for _, c := range g.Options {
						opt.Choices = append(opt.Choices, &inabav1.SessionOption_Choice{Value: string(c.Value), Name: c.Name, Description: text(c.Description), Group: g.Name})
					}
				}
			}
			out = append(out, opt)
		}
	}
	s.mu.Lock()
	s.booleans = booleans
	s.mu.Unlock()
	s.emit(&inabav1.Event{Payload: &inabav1.Event_OptionsChanged{OptionsChanged: &inabav1.OptionsChanged{Options: out}}})
}

func category(c *sdk.SessionConfigOptionCategory) string {
	if c == nil {
		return ""
	}
	return string(*c)
}

func (s *session) reportMode(current string) {
	s.mu.Lock()
	modes := s.modes
	if modes != nil {
		modes.CurrentModeId = sdk.SessionModeId(current)
	}
	s.mu.Unlock()
	if modes == nil {
		return
	}
	opt := &inabav1.SessionOption{Id: modeOptionID, Name: "Mode", Category: "mode", Value: current}
	for _, m := range modes.AvailableModes {
		opt.Choices = append(opt.Choices, &inabav1.SessionOption_Choice{Value: string(m.Id), Name: m.Name, Description: text(m.Description)})
	}
	s.emit(&inabav1.Event{Payload: &inabav1.Event_OptionsChanged{OptionsChanged: &inabav1.OptionsChanged{Options: []*inabav1.SessionOption{opt}}}})
}

func (s *session) SetOption(ctx context.Context, id, value string) error {
	s.mu.Lock()
	modes, boolean := s.modes, s.booleans[id]
	s.mu.Unlock()
	if modes != nil {
		if id != modeOptionID {
			return fmt.Errorf("acp: no option %q", id)
		}
		if _, err := s.conn.SetSessionMode(ctx, sdk.SetSessionModeRequest{SessionId: s.id, ModeId: sdk.SessionModeId(value)}); err != nil {
			return err
		}
		// Not every agent confirms with an update; report what was set.
		s.reportMode(value)
		return nil
	}
	req := sdk.SetSessionConfigOptionRequest{ValueId: &sdk.SetSessionConfigOptionValueId{SessionId: s.id, ConfigId: sdk.SessionConfigId(id), Value: sdk.SessionConfigValueId(value)}}
	if boolean {
		// An on/off option travels as a JSON boolean, marked as such.
		req = sdk.SetSessionConfigOptionRequest{Boolean: &sdk.SetSessionConfigOptionBoolean{SessionId: s.id, ConfigId: sdk.SessionConfigId(id), Type: "boolean", Value: value == "true"}}
	}
	res, err := s.conn.SetSessionConfigOption(ctx, req)
	if err != nil {
		return err
	}
	// The answer is the full set: changing one option can change what others offer.
	if len(res.ConfigOptions) > 0 {
		s.reportConfig(res.ConfigOptions)
	}
	return nil
}

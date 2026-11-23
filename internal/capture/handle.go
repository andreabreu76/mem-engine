package capture

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/andreabreu76/mem-engine/internal/config"
	"github.com/andreabreu76/mem-engine/internal/project"
	"github.com/andreabreu76/mem-engine/internal/redact"
	"github.com/andreabreu76/mem-engine/internal/spool"
	"github.com/andreabreu76/mem-engine/internal/store"
)

const (
	maxPayloadBytes    = 32 << 20
	sessionEndTimeout  = 1500 * time.Millisecond
	sessionEndBusyWait = 500 * time.Millisecond
	spoolEvent         = "spool"
)

type Deps struct {
	Cfg                config.Config
	Log                *slog.Logger
	Now                func() time.Time
	StoreOptions       store.Options
	SessionStartOutput func(ctx context.Context, st *store.Store, op Op, p Payload) string
}

type handler struct {
	event   string
	d       Deps
	log     *slog.Logger
	now     func() time.Time
	opts    store.Options
	st      *store.Store
	openErr error
	opened  bool
}

func Handle(ctx context.Context, event string, stdin io.Reader, stdout io.Writer, d Deps) error {
	h := &handler{event: event, d: d, log: d.Log, now: d.Now, opts: d.StoreOptions}
	if h.log == nil {
		h.log = slog.New(slog.DiscardHandler)
	}
	if h.now == nil {
		h.now = time.Now
	}
	if event == eventSessionEnd {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, sessionEndTimeout)
		defer cancel()
		if h.opts.BusyTimeout <= 0 || h.opts.BusyTimeout > sessionEndBusyWait {
			h.opts.BusyTimeout = sessionEndBusyWait
		}
	}
	defer h.close()
	return h.run(ctx, stdin, stdout)
}

func (h *handler) run(ctx context.Context, stdin io.Reader, stdout io.Writer) error {
	data, err := io.ReadAll(io.LimitReader(stdin, maxPayloadBytes))
	if err != nil {
		return h.fail(ctx, "", "read hook payload failed")
	}
	var p Payload
	if err := json.Unmarshal(data, &p); err != nil {
		return h.fail(ctx, "", "hook payload is not valid JSON")
	}
	r, err := redact.New(h.d.Cfg.RedactExtra, h.d.Cfg.RedactDisableBuiltin)
	if err != nil {
		return h.fail(ctx, p.SessionID, "invalid redact_extra pattern in config.toml")
	}
	proj, err := project.Resolve(p.Cwd, h.d.Cfg)
	if err != nil {
		return h.fail(ctx, p.SessionID, "resolve project from cwd failed")
	}
	op, keep, err := Build(h.event, p, proj, h.d.Cfg, r, h.now())
	if err != nil {
		return h.fail(ctx, p.SessionID, "unsupported hook event")
	}
	if !keep {
		return nil
	}
	st, err := h.store()
	if err != nil {
		h.log.Warn("store unavailable, event spooled", "event", h.event, "session_id", p.SessionID, "error", err.Error())
		return h.spool(ctx, op)
	}
	if err := h.capture(ctx, st, op); err != nil {
		h.log.Warn("store write failed, event spooled", "event", h.event, "session_id", p.SessionID, "error", err.Error())
		if err := h.spool(ctx, op); err != nil {
			return err
		}
	}
	if h.event == eventSessionStart {
		h.sessionStart(ctx, st, op, p, stdout)
	}
	return nil
}

func (h *handler) capture(ctx context.Context, st *store.Store, op Op) error {
	if err := h.drain(ctx, st); err != nil {
		return err
	}
	return apply(ctx, st, op)
}

func (h *handler) sessionStart(ctx context.Context, st *store.Store, op Op, p Payload, stdout io.Writer) {
	if h.d.SessionStartOutput == nil {
		return
	}
	text := h.d.SessionStartOutput(ctx, st, op, p)
	if text == "" {
		return
	}
	out := struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}{}
	out.HookSpecificOutput.HookEventName = eventSessionStart
	out.HookSpecificOutput.AdditionalContext = text
	enc := json.NewEncoder(stdout)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(out); err != nil {
		h.log.Warn("write session start output failed", "event", h.event, "session_id", p.SessionID, "error", err.Error())
	}
}

func (h *handler) drain(ctx context.Context, st *store.Store) error {
	_, err := spool.Drain(h.d.Cfg.SpoolDir(), func(line json.RawMessage) error {
		var op Op
		if err := json.Unmarshal(line, &op); err != nil {
			return h.discard(ctx, st, "", "spool line is not valid JSON, discarded")
		}
		if msg := validate(op); msg != "" {
			return h.discard(ctx, st, op.Session.SessionID, msg+", discarded")
		}
		return apply(ctx, st, op)
	})
	return err
}

func (h *handler) discard(ctx context.Context, st *store.Store, sessionID, msg string) error {
	h.log.Error("capture failed", "event", spoolEvent, "session_id", sessionID, "error", msg)
	return st.RecordCaptureError(ctx, spoolEvent, msg, h.now())
}

func validate(op Op) string {
	switch {
	case op.Project.Key == "" || op.Session.Project == "":
		return "spool op has no project"
	case op.Obs != nil && !kinds[op.Obs.Kind]:
		return "spool op has an unknown observation kind"
	}
	return ""
}

func apply(ctx context.Context, st *store.Store, op Op) error {
	if err := st.EnsureProject(ctx, op.Project); err != nil {
		return err
	}
	if err := st.TouchSession(ctx, op.Session); err != nil {
		return err
	}
	if op.Obs == nil {
		return nil
	}
	_, err := st.InsertObservation(ctx, *op.Obs)
	return err
}

func (h *handler) spool(ctx context.Context, op Op) error {
	if err := spool.Append(h.d.Cfg.SpoolDir(), op); err != nil {
		return h.fail(ctx, op.Session.SessionID, "spool write failed: "+err.Error())
	}
	return nil
}

func (h *handler) fail(ctx context.Context, sessionID, msg string) error {
	h.log.Error("capture failed", "event", h.event, "session_id", sessionID, "error", msg)
	if st, err := h.store(); err == nil {
		if err := st.RecordCaptureError(ctx, h.event, msg, h.now()); err != nil {
			h.log.Error("record capture error failed", "event", h.event, "session_id", sessionID, "error", err.Error())
		}
	}
	return errors.New(msg)
}

func (h *handler) store() (*store.Store, error) {
	if !h.opened {
		h.opened = true
		if err := os.MkdirAll(h.d.Cfg.Home, 0o700); err != nil {
			h.openErr = err
		} else {
			h.st, h.openErr = store.OpenWith(h.d.Cfg.DBPath(), h.opts)
		}
	}
	return h.st, h.openErr
}

func (h *handler) close() {
	if h.st != nil {
		h.st.Close()
	}
}

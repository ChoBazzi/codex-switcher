package usage

import (
	"encoding/json"
	"math"
	"time"
)

const MaxBytes = 128 << 10

type Window struct {
	UsedPercent      *float64   `json:"used_percent"`
	RemainingPercent *float64   `json:"remaining_percent"`
	LimitSeconds     *int64     `json:"limit_seconds"`
	ResetAt          *time.Time `json:"reset_at"`
}

type Data struct {
	Primary          Window   `json:"primary"`
	Secondary        Window   `json:"secondary"`
	Allowed          *bool    `json:"allowed"`
	LimitReached     *bool    `json:"limit_reached"`
	RemainingPercent *float64 `json:"remaining_percent"`
	// This is an observation, not a Wiki command or a deduplicated event.
	CheckpointLevel string `json:"checkpoint_level"`
}

type rawWindow struct {
	Used    *float64 `json:"used_percent"`
	Seconds *int64   `json:"limit_window_seconds"`
	Reset   *int64   `json:"reset_at"`
}

func Parse(data []byte) (Data, error) {
	var body struct {
		Rate json.RawMessage `json:"rate_limit"`
	}
	if len(data) > MaxBytes || json.Unmarshal(data, &body) != nil || len(body.Rate) == 0 {
		return Data{}, failure("usage_schema_unsupported", 0)
	}
	var rate struct {
		Primary   *rawWindow `json:"primary_window"`
		Secondary *rawWindow `json:"secondary_window"`
		Allowed   *bool      `json:"allowed"`
		Reached   *bool      `json:"limit_reached"`
	}
	if json.Unmarshal(body.Rate, &rate) != nil {
		return Data{}, failure("usage_schema_unsupported", 0)
	}
	primary, err := normalize(rate.Primary)
	if err != nil {
		return Data{}, err
	}
	secondary, err := normalize(rate.Secondary)
	if err != nil {
		return Data{}, err
	}
	d := Data{Primary: primary, Secondary: secondary, Allowed: rate.Allowed, LimitReached: rate.Reached, CheckpointLevel: "unknown"}
	if primary.RemainingPercent != nil && secondary.RemainingPercent != nil {
		remaining := math.Min(*primary.RemainingPercent, *secondary.RemainingPercent)
		d.RemainingPercent = &remaining
		d.CheckpointLevel = "above_50"
		if remaining <= 50 {
			d.CheckpointLevel = "at_or_below_50"
		}
		if remaining <= 10 {
			d.CheckpointLevel = "at_or_below_10"
		}
	}
	return d, nil
}

func normalize(w *rawWindow) (Window, error) {
	if w == nil {
		return Window{}, nil
	}
	result := Window{UsedPercent: w.Used, LimitSeconds: w.Seconds}
	if w.Used != nil {
		if math.IsNaN(*w.Used) || math.IsInf(*w.Used, 0) || *w.Used < 0 || *w.Used > 100 {
			return Window{}, failure("usage_schema_unsupported", 0)
		}
		remaining := 100 - *w.Used
		result.RemainingPercent = &remaining
	}
	if w.Seconds != nil && *w.Seconds <= 0 {
		return Window{}, failure("usage_schema_unsupported", 0)
	}
	if w.Reset != nil {
		if *w.Reset <= 0 || *w.Reset > 253402300799 {
			return Window{}, failure("usage_schema_unsupported", 0)
		}
		reset := time.Unix(*w.Reset, 0).UTC()
		result.ResetAt = &reset
	}
	return result, nil
}

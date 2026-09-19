// Package zen holds the ZenMoney diff-protocol client and the in-memory
// snapshot the MCP tools read from.
package zen

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// defaultAPIBase is the live ZenMoney host. ZENMONEY_API_BASE overrides it so
// tests can point the client at a local stub.
const defaultAPIBase = "https://api.zenmoney.ru"

// Deletion is an object the client asks the server to delete (or that the
// server reports as deleted). Object is the entity name used by the diff
// protocol — "transaction", "account", "tag", "merchant", "reminder", …
type Deletion struct {
	ID     string `json:"id"`
	Object string `json:"object"`
	Stamp  int64  `json:"stamp"`
	User   int64  `json:"user"`
}

// DiffRequest is one call to /v8/diff/. Every entity slice is omitted unless
// the caller is pushing changes of that kind.
type DiffRequest struct {
	CurrentClientTimestamp int64            `json:"currentClientTimestamp"`
	ServerTimestamp        int64            `json:"serverTimestamp"`
	ForceFetch             []string         `json:"forceFetch,omitempty"`
	Transaction            []Transaction    `json:"transaction,omitempty"`
	Reminder               []Reminder       `json:"reminder,omitempty"`
	ReminderMarker         []ReminderMarker `json:"reminderMarker,omitempty"`
	Deletion               []Deletion       `json:"deletion,omitempty"`
}

// DiffResponse is everything that changed since the request's serverTimestamp.
type DiffResponse struct {
	ServerTimestamp int64            `json:"serverTimestamp"`
	Instrument      []Instrument     `json:"instrument"`
	Company         []Company        `json:"company"`
	User            []User           `json:"user"`
	Account         []Account        `json:"account"`
	Tag             []Tag            `json:"tag"`
	Merchant        []Merchant       `json:"merchant"`
	Reminder        []Reminder       `json:"reminder"`
	ReminderMarker  []ReminderMarker `json:"reminderMarker"`
	Transaction     []Transaction    `json:"transaction"`
	Deletion        []Deletion       `json:"deletion"`
}

type Instrument struct {
	ID         int64   `json:"id"`
	Changed    int64   `json:"changed"`
	Title      string  `json:"title"`
	ShortTitle string  `json:"shortTitle"`
	Symbol     string  `json:"symbol"`
	Rate       float64 `json:"rate"`
}

type User struct {
	ID          int64  `json:"id"`
	Changed     int64  `json:"changed"`
	Login       string `json:"login"`
	Currency    int64  `json:"currency"`
	Parent      *int64 `json:"parent"`
	Country     int64  `json:"country"`
	CountryCode string `json:"countryCode"`
	Email       string `json:"email"`
}

type Account struct {
	ID               string   `json:"id"`
	Changed          int64    `json:"changed"`
	User             int64    `json:"user"`
	Instrument       *int64   `json:"instrument"`
	Company          *int64   `json:"company"`
	Type             string   `json:"type"`
	Title            string   `json:"title"`
	SyncID           []string `json:"syncID"`
	Balance          *float64 `json:"balance"`
	StartBalance     *float64 `json:"startBalance"`
	CreditLimit      *float64 `json:"creditLimit"`
	InBalance        bool     `json:"inBalance"`
	Savings          *bool    `json:"savings"`
	EnableCorrection bool     `json:"enableCorrection"`
	EnableSMS        bool     `json:"enableSMS"`
	Archive          bool     `json:"archive"`
	Private          bool     `json:"private"`
}

type Tag struct {
	ID            string  `json:"id"`
	Changed       int64   `json:"changed"`
	User          int64   `json:"user"`
	Title         string  `json:"title"`
	Parent        *string `json:"parent"`
	Icon          *string `json:"icon"`
	Picture       *string `json:"picture"`
	Color         *int64  `json:"color"`
	ShowIncome    bool    `json:"showIncome"`
	ShowOutcome   bool    `json:"showOutcome"`
	BudgetIncome  bool    `json:"budgetIncome"`
	BudgetOutcome bool    `json:"budgetOutcome"`
	Required      *bool   `json:"required"`
}

type Merchant struct {
	ID      string `json:"id"`
	Changed int64  `json:"changed"`
	User    int64  `json:"user"`
	Title   string `json:"title"`
}

type Company struct {
	ID        int64   `json:"id"`
	Changed   int64   `json:"changed"`
	Title     string  `json:"title"`
	Country   *int64  `json:"country"`
	FullTitle *string `json:"fullTitle"`
	WWW       *string `json:"www"`
}

// Transaction mirrors the diff protocol field for field. Nullable fields are
// pointers without omitempty so a cleared value marshals as an explicit null,
// the way the TypeScript server sends it.
type Transaction struct {
	ID                  string   `json:"id"`
	Changed             int64    `json:"changed"`
	Created             int64    `json:"created"`
	User                int64    `json:"user"`
	Deleted             bool     `json:"deleted"`
	Hold                *bool    `json:"hold"`
	Viewed              bool     `json:"viewed"`
	IncomeInstrument    int64    `json:"incomeInstrument"`
	IncomeAccount       string   `json:"incomeAccount"`
	Income              float64  `json:"income"`
	IncomeBankID        *string  `json:"incomeBankID"`
	OutcomeInstrument   int64    `json:"outcomeInstrument"`
	OutcomeAccount      string   `json:"outcomeAccount"`
	Outcome             float64  `json:"outcome"`
	OutcomeBankID       *string  `json:"outcomeBankID"`
	OpIncome            *float64 `json:"opIncome"`
	OpIncomeInstrument  *int64   `json:"opIncomeInstrument"`
	OpOutcome           *float64 `json:"opOutcome"`
	OpOutcomeInstrument *int64   `json:"opOutcomeInstrument"`
	Tag                 []string `json:"tag"`
	Merchant            *string  `json:"merchant"`
	Payee               *string  `json:"payee"`
	OriginalPayee       *string  `json:"originalPayee"`
	Comment             *string  `json:"comment"`
	Date                string   `json:"date"`
	MCC                 *int64   `json:"mcc"`
	Latitude            *float64 `json:"latitude"`
	Longitude           *float64 `json:"longitude"`
	ReminderMarker      *string  `json:"reminderMarker"`
	QRCode              *string  `json:"qrCode"`
}

// Reminder is a planned transaction: either a one-off entry dated in the
// future or the template of a repeating series. ZenMoney expands it into
// ReminderMarker occurrences, so the reminder carries the schedule, not the
// dates.
type Reminder struct {
	ID                string   `json:"id"`
	Changed           int64    `json:"changed"`
	User              int64    `json:"user"`
	IncomeInstrument  int64    `json:"incomeInstrument"`
	IncomeAccount     string   `json:"incomeAccount"`
	Income            float64  `json:"income"`
	OutcomeInstrument int64    `json:"outcomeInstrument"`
	OutcomeAccount    string   `json:"outcomeAccount"`
	Outcome           float64  `json:"outcome"`
	Tag               []string `json:"tag"`
	Merchant          *string  `json:"merchant"`
	Payee             *string  `json:"payee"`
	Comment           *string  `json:"comment"`
	// Interval is "day", "week", "month" or "year", or nil for a one-off.
	Interval *string `json:"interval"`
	// Step is how many intervals between repeats: 2 with "week" means fortnightly.
	Step *int64 `json:"step"`
	// Points are positions inside the step window the series fires on, counted
	// in Interval units from StartDate and zero-based, so every point is below
	// Step. The documented example — interval "day", step 7, points [0, 2, 4] —
	// repeats weekly on the start weekday and two and four days after it. [0]
	// on its own means "once per window".
	//
	// add_reminder writes this field and walks it to produce the occurrences:
	// ZenMoney does not expand a series, so the markers are the client's to
	// create. Nothing reads the field back — dates come from the markers.
	Points    []int64 `json:"points"`
	StartDate string  `json:"startDate"`
	EndDate   *string `json:"endDate"`
	Notify    bool    `json:"notify"`
}

// ReminderMarker is one occurrence of a reminder on a specific date.
type ReminderMarker struct {
	ID                string   `json:"id"`
	Changed           int64    `json:"changed"`
	User              int64    `json:"user"`
	IncomeInstrument  int64    `json:"incomeInstrument"`
	IncomeAccount     string   `json:"incomeAccount"`
	Income            float64  `json:"income"`
	OutcomeInstrument int64    `json:"outcomeInstrument"`
	OutcomeAccount    string   `json:"outcomeAccount"`
	Outcome           float64  `json:"outcome"`
	Tag               []string `json:"tag"`
	Merchant          *string  `json:"merchant"`
	Payee             *string  `json:"payee"`
	Comment           *string  `json:"comment"`
	Date              string   `json:"date"`
	// Reminder is the id of the series this occurrence belongs to.
	Reminder string `json:"reminder"`
	// State is "planned" until the occurrence is turned into a transaction or
	// dismissed.
	State      string `json:"state"`
	Notify     bool   `json:"notify"`
	IsForecast *bool  `json:"isForecast,omitempty"`
}

type SuggestRequest struct {
	Payee    string `json:"payee,omitempty"`
	Merchant string `json:"merchant,omitempty"`
}

type SuggestResponse struct {
	Tag      []string `json:"tag"`
	Merchant *string  `json:"merchant"`
	Payee    *string  `json:"payee"`
}

// API is a ZenMoney diff-protocol client. It is safe for concurrent use.
type API struct {
	token  string
	base   string
	client *http.Client
}

func NewAPI(token string) *API {
	base := os.Getenv("ZENMONEY_API_BASE")
	if base == "" {
		base = defaultAPIBase
	}
	return &API{
		token: token,
		base:  strings.TrimSuffix(base, "/"),
		// A full sync downloads every transaction ever recorded, so the
		// timeout has to cover a slow first run on a slow link.
		client: &http.Client{Timeout: 120 * time.Second},
	}
}

func (a *API) request(ctx context.Context, path string, body, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.base+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.token)

	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		text, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		return fmt.Errorf("ZenMoney API error %d: %s", resp.StatusCode, string(text))
	}

	return json.NewDecoder(resp.Body).Decode(out)
}

func (a *API) Diff(ctx context.Context, req DiffRequest) (*DiffResponse, error) {
	var resp DiffResponse
	if err := a.request(ctx, "/v8/diff/", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (a *API) Suggest(ctx context.Context, items []SuggestRequest) ([]SuggestResponse, error) {
	var resp []SuggestResponse
	if err := a.request(ctx, "/v8/suggest/", items, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

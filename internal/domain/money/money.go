package money

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

var (
	ErrInvalidAmount    = errors.New("invalid monetary amount")
	ErrNegativeAmount   = errors.New("negative monetary amount")
	ErrCurrencyMismatch = errors.New("currency mismatch")
	ErrOverflow         = errors.New("monetary overflow")
)

type Money struct {
	minor    int64
	currency Currency
}

func New(minor int64, currency Currency) (Money, error) {
	if !currency.Valid() {
		return Money{}, ErrInvalidCurrency
	}
	return Money{minor: minor, currency: currency}, nil
}

func Zero(currency Currency) (Money, error) {
	return New(0, currency)
}

// Parse accepts a signed decimal for internal calculations. External inputs
// must use ParseNonNegative.
func Parse(amount, currencyCode string) (Money, error) {
	currency, err := ParseCurrency(currencyCode)
	if err != nil {
		return Money{}, err
	}
	minor, err := parseMinorUnits(amount)
	if err != nil {
		return Money{}, err
	}
	return New(minor, currency)
}

func ParseNonNegative(amount, currencyCode string) (Money, error) {
	if strings.HasPrefix(amount, "-") {
		return Money{}, ErrNegativeAmount
	}
	value, err := Parse(amount, currencyCode)
	if err != nil {
		return Money{}, err
	}
	return value, nil
}

func (m Money) Validate() error {
	if !m.currency.Valid() {
		return ErrInvalidCurrency
	}
	return nil
}

func (m Money) MinorUnits() int64 {
	return m.minor
}

func (m Money) Currency() Currency {
	return m.currency
}

func (m Money) IsZero() bool {
	return m.minor == 0 && m.currency.Valid()
}

func (m Money) IsPositive() bool {
	return m.minor > 0 && m.currency.Valid()
}

func (m Money) IsNegative() bool {
	return m.minor < 0 && m.currency.Valid()
}

func (m Money) Equal(other Money) bool {
	return m.minor == other.minor && m.currency == other.currency && m.currency.Valid()
}

func (m Money) Add(other Money) (Money, error) {
	if err := compatible(m, other); err != nil {
		return Money{}, err
	}
	if (other.minor > 0 && m.minor > math.MaxInt64-other.minor) ||
		(other.minor < 0 && m.minor < math.MinInt64-other.minor) {
		return Money{}, ErrOverflow
	}
	return New(m.minor+other.minor, m.currency)
}

func (m Money) Subtract(other Money) (Money, error) {
	if err := compatible(m, other); err != nil {
		return Money{}, err
	}
	if (other.minor < 0 && m.minor > math.MaxInt64+other.minor) ||
		(other.minor > 0 && m.minor < math.MinInt64+other.minor) {
		return Money{}, ErrOverflow
	}
	return New(m.minor-other.minor, m.currency)
}

func (m Money) Negate() (Money, error) {
	if err := m.Validate(); err != nil {
		return Money{}, err
	}
	if m.minor == math.MinInt64 {
		return Money{}, ErrOverflow
	}
	return New(-m.minor, m.currency)
}

func (m Money) Compare(other Money) (int, error) {
	if err := compatible(m, other); err != nil {
		return 0, err
	}
	switch {
	case m.minor < other.minor:
		return -1, nil
	case m.minor > other.minor:
		return 1, nil
	default:
		return 0, nil
	}
}

func (m Money) String() string {
	if m.Validate() != nil {
		return ""
	}

	negative := m.minor < 0
	var magnitude uint64
	if negative {
		magnitude = uint64(-(m.minor + 1)) + 1
	} else {
		magnitude = uint64(m.minor)
	}

	prefix := ""
	if negative {
		prefix = "-"
	}
	return fmt.Sprintf("%s%d.%02d", prefix, magnitude/100, magnitude%100)
}

func (m Money) MarshalJSON() ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Amount   string `json:"amount"`
		Currency string `json:"currency"`
	}{Amount: m.String(), Currency: m.currency.Code()})
}

func compatible(left, right Money) error {
	if err := left.Validate(); err != nil {
		return err
	}
	if err := right.Validate(); err != nil {
		return err
	}
	if left.currency != right.currency {
		return fmt.Errorf("%w: %s and %s", ErrCurrencyMismatch, left.currency.Code(), right.currency.Code())
	}
	return nil
}

func parseMinorUnits(raw string) (int64, error) {
	if raw == "" || strings.TrimSpace(raw) != raw || strings.HasPrefix(raw, "+") {
		return 0, ErrInvalidAmount
	}

	negative := strings.HasPrefix(raw, "-")
	digits := raw
	if negative {
		digits = strings.TrimPrefix(raw, "-")
	}
	parts := strings.Split(digits, ".")
	if len(parts) != 2 || parts[0] == "" || len(parts[1]) != 2 || !allDigits(parts[0]) || !allDigits(parts[1]) {
		return 0, ErrInvalidAmount
	}

	combined := strings.TrimLeft(parts[0]+parts[1], "0")
	if combined == "" {
		combined = "0"
	}
	if negative {
		combined = "-" + combined
	}
	minor, err := strconv.ParseInt(combined, 10, 64)
	if err != nil {
		if errors.Is(err, strconv.ErrRange) {
			return 0, ErrOverflow
		}
		return 0, ErrInvalidAmount
	}
	return minor, nil
}

func allDigits(value string) bool {
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

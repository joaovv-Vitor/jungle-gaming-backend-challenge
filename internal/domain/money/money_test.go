package money

import (
	"encoding/json"
	"errors"
	"math"
	"testing"
)

func TestParseAndSerialize(t *testing.T) {
	tests := []struct {
		input string
		minor int64
		want  string
	}{
		{input: "0.00", minor: 0, want: "0.00"},
		{input: "25.00", minor: 2500, want: "25.00"},
		{input: "00025.09", minor: 2509, want: "25.09"},
		{input: "-10.15", minor: -1015, want: "-10.15"},
		{input: "92233720368547758.07", minor: math.MaxInt64, want: "92233720368547758.07"},
		{input: "-92233720368547758.08", minor: math.MinInt64, want: "-92233720368547758.08"},
	}

	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			value, err := Parse(test.input, "BRL")
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if value.MinorUnits() != test.minor || value.String() != test.want {
				t.Fatalf("Parse() = %d/%q, want %d/%q", value.MinorUnits(), value.String(), test.minor, test.want)
			}
		})
	}
}

func TestParseRejectsInvalidValues(t *testing.T) {
	for _, input := range []string{"", "25", "25.0", "25.000", ".00", "1.", "+1.00", " 1.00", "1.00 ", "1e2", "NaN", "Infinity", "--1.00", "1,00"} {
		t.Run(input, func(t *testing.T) {
			_, err := Parse(input, "BRL")
			if !errors.Is(err, ErrInvalidAmount) {
				t.Fatalf("Parse(%q) error = %v, want ErrInvalidAmount", input, err)
			}
		})
	}
}

func TestParseDetectsOverflow(t *testing.T) {
	for _, input := range []string{"92233720368547758.08", "-92233720368547758.09"} {
		_, err := Parse(input, "BRL")
		if !errors.Is(err, ErrOverflow) {
			t.Fatalf("Parse(%q) error = %v, want ErrOverflow", input, err)
		}
	}
}

func TestParseNonNegativeRejectsNegative(t *testing.T) {
	for _, input := range []string{"-0.01", "-0.00"} {
		_, err := ParseNonNegative(input, "BRL")
		if !errors.Is(err, ErrNegativeAmount) {
			t.Fatalf("ParseNonNegative(%q) error = %v, want ErrNegativeAmount", input, err)
		}
	}
}

func TestArithmetic(t *testing.T) {
	left := mustParse(t, "10.00", "BRL")
	right := mustParse(t, "2.50", "BRL")

	sum, err := left.Add(right)
	if err != nil || sum.String() != "12.50" {
		t.Fatalf("Add() = %q, %v", sum.String(), err)
	}
	difference, err := left.Subtract(right)
	if err != nil || difference.String() != "7.50" {
		t.Fatalf("Subtract() = %q, %v", difference.String(), err)
	}
	negative, err := right.Negate()
	if err != nil || negative.String() != "-2.50" {
		t.Fatalf("Negate() = %q, %v", negative.String(), err)
	}
	comparison, err := left.Compare(right)
	if err != nil || comparison != 1 {
		t.Fatalf("Compare() = %d, %v", comparison, err)
	}
}

func TestArithmeticRejectsCurrencyMismatch(t *testing.T) {
	brl := mustParse(t, "1.00", "BRL")
	usd := mustParse(t, "1.00", "USD")
	_, err := brl.Add(usd)
	if !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatalf("Add() error = %v, want ErrCurrencyMismatch", err)
	}
}

func TestArithmeticDetectsOverflow(t *testing.T) {
	currency, err := ParseCurrency("BRL")
	if err != nil {
		t.Fatal(err)
	}
	maximum, _ := New(math.MaxInt64, currency)
	minimum, _ := New(math.MinInt64, currency)
	one, _ := New(1, currency)
	minusOne, _ := New(-1, currency)

	assertErrorIs(t, func() error { _, err := maximum.Add(one); return err }(), ErrOverflow)
	assertErrorIs(t, func() error { _, err := minimum.Add(minusOne); return err }(), ErrOverflow)
	assertErrorIs(t, func() error { _, err := minimum.Subtract(one); return err }(), ErrOverflow)
	assertErrorIs(t, func() error { _, err := maximum.Subtract(minusOne); return err }(), ErrOverflow)
	assertErrorIs(t, func() error { _, err := minimum.Negate(); return err }(), ErrOverflow)
}

func TestZeroValueAndInvalidCurrencyAreRejected(t *testing.T) {
	if err := (Money{}).Validate(); !errors.Is(err, ErrInvalidCurrency) {
		t.Fatalf("zero Money validation error = %v", err)
	}
	if _, err := Parse("1.00", "XYZ"); !errors.Is(err, ErrInvalidCurrency) {
		t.Fatalf("invalid currency error = %v", err)
	}
}

func TestMarshalJSONUsesDecimalString(t *testing.T) {
	value := mustParse(t, "25.10", "BRL")
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"amount":"25.10","currency":"BRL"}` {
		t.Fatalf("MarshalJSON() = %s", encoded)
	}
}

func mustParse(t *testing.T, amount, currency string) Money {
	t.Helper()
	value, err := Parse(amount, currency)
	if err != nil {
		t.Fatalf("Parse(%q, %q): %v", amount, currency, err)
	}
	return value
}

func assertErrorIs(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("error = %v, want %v", err, target)
	}
}

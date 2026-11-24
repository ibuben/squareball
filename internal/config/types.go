package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ByteSize — размер в байтах, в YAML пишется как "25MB", "512KB" или число.
type ByteSize int64

func ParseByteSize(s string) (ByteSize, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	mult := int64(1)
	for _, u := range []struct {
		suf string
		m   int64
	}{{"GB", 1 << 30}, {"MB", 1 << 20}, {"KB", 1 << 10}, {"B", 1}} {
		if strings.HasSuffix(s, u.suf) {
			mult = u.m
			s = strings.TrimSpace(strings.TrimSuffix(s, u.suf))
			break
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	return ByteSize(n * mult), nil
}

func (b *ByteSize) UnmarshalYAML(n *yaml.Node) error {
	v, err := ParseByteSize(n.Value)
	if err != nil {
		return err
	}
	*b = v
	return nil
}

func (b ByteSize) MarshalYAML() (any, error) {
	switch {
	case b%(1<<20) == 0 && b != 0:
		return fmt.Sprintf("%dMB", b/(1<<20)), nil
	case b%(1<<10) == 0 && b != 0:
		return fmt.Sprintf("%dKB", b/(1<<10)), nil
	}
	return int64(b), nil
}

// Rate — лимит вида "20/10m": не более 20 событий за 10 минут.
type Rate struct {
	N      int
	Period time.Duration
}

func ParseRate(s string) (Rate, error) {
	parts := strings.SplitN(strings.TrimSpace(s), "/", 2)
	if len(parts) != 2 {
		return Rate{}, fmt.Errorf("invalid rate %q, want N/period", s)
	}
	n, err := strconv.Atoi(parts[0])
	if err != nil || n <= 0 {
		return Rate{}, fmt.Errorf("invalid rate %q", s)
	}
	p := parts[1]
	if p != "" && (p[0] < '0' || p[0] > '9') {
		p = "1" + p // "100/h" → 1h
	}
	d, err := time.ParseDuration(p)
	if err != nil || d <= 0 {
		return Rate{}, fmt.Errorf("invalid rate period %q", s)
	}
	return Rate{N: n, Period: d}, nil
}

func (r *Rate) UnmarshalYAML(n *yaml.Node) error {
	v, err := ParseRate(n.Value)
	if err != nil {
		return err
	}
	*r = v
	return nil
}

func (r Rate) MarshalYAML() (any, error) {
	if r.N == 0 {
		return "", nil
	}
	return fmt.Sprintf("%d/%s", r.N, shortDur(r.Period)), nil
}

func (r Rate) String() string { v, _ := r.MarshalYAML(); return fmt.Sprint(v) }

func shortDur(d time.Duration) string {
	s := d.String()
	s = strings.TrimSuffix(s, "0s")
	s = strings.TrimSuffix(s, "0m")
	if s == "" {
		return "0s"
	}
	return s
}

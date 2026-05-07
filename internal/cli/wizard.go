package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ljagiello/yamaha-cli/internal/config"
	"github.com/ljagiello/yamaha-cli/pkg/discover"
)

// wizardScanTimeout matches the first-run flow's documented 3 s SSDP wait.
const wizardScanTimeout = 3 * time.Second

// runWizard executes the first-run flow described in PLAN.v6 ("First-run
// flow"). It writes prompts to out (typically stdout) and progress
// chatter to errOut (stderr), reads answers from os.Stdin, and saves the
// chosen device to disk.
//
// On zero devices found returns &unreachableError (which maps to exit 69).
// Returns the resolved alias and Device on success.
func runWizard(ctx context.Context, out, errOut io.Writer, cfg *config.Config) (string, config.Device, error) {
	if cfg == nil {
		cfg = &config.Config{}
	}

	fmt.Fprintln(errOut, "No device configured. Searching the LAN…")
	devs, err := discover.Search(ctx, wizardScanTimeout)
	if err != nil {
		return "", config.Device{}, fmt.Errorf("LAN search failed: %w", err)
	}
	if len(devs) == 0 {
		return "", config.Device{}, &unreachableError{
			cause: fmt.Errorf("no Yamaha devices found on LAN; pass --host <ip> manually"),
		}
	}

	reader := bufio.NewReader(os.Stdin)

	var picked discover.Device
	switch len(devs) {
	case 1:
		picked = devs[0]
		fmt.Fprintf(out, "Found 1 Yamaha device: %s (%s, %s)\n", picked.Name, picked.Model, picked.Host)
		ans, err := promptDefault(out, reader, "Use this device? [Y/n]", "y")
		if err != nil {
			return "", config.Device{}, err
		}
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(ans)), "n") {
			return "", config.Device{}, fmt.Errorf("wizard aborted by user")
		}
	default:
		fmt.Fprintf(out, "Found %d Yamaha devices:\n", len(devs))
		for i, d := range devs {
			fmt.Fprintf(out, "  [%d] %s (%s, %s)\n", i+1, d.Name, d.Model, d.Host)
		}
		idx, err := promptIndex(out, reader, len(devs))
		if err != nil {
			return "", config.Device{}, err
		}
		picked = devs[idx]
	}

	defaultAlias := uniqueAlias(slugify(picked.Name), cfg)
	prompt := fmt.Sprintf("Alias for this device [%s]", defaultAlias)
	rawAlias, err := promptDefault(out, reader, prompt, defaultAlias)
	if err != nil {
		return "", config.Device{}, err
	}
	alias := strings.TrimSpace(rawAlias)
	if alias == "" {
		alias = defaultAlias
	}
	// If the user chose an alias that collides with an existing one,
	// suggest a numbered suffix and confirm rather than silently overwrite.
	if _, exists := cfg.Devices[alias]; exists {
		suggested := uniqueAlias(alias, cfg)
		ans, perr := promptDefault(
			out, reader,
			fmt.Sprintf("Alias %q already exists; use %q instead? [Y/n]", alias, suggested),
			"y",
		)
		if perr != nil {
			return "", config.Device{}, perr
		}
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(ans)), "n") {
			return "", config.Device{}, fmt.Errorf("alias %q already exists; pick another", alias)
		}
		alias = suggested
	}

	dev := config.Device{
		Host:        picked.Host,
		UDN:         picked.UDN,
		DefaultZone: "main",
	}

	if cfg.Devices == nil {
		cfg.Devices = map[string]config.Device{}
	}
	cfg.Devices[alias] = dev

	// First device added → make it the default.
	if cfg.DefaultDevice == "" {
		cfg.DefaultDevice = alias
	}

	if err := config.Save(cfg); err != nil {
		return "", config.Device{}, fmt.Errorf("save config: %w", err)
	}

	fmt.Fprintf(errOut, "Saved %s → %s (%s)\n", alias, picked.Host, config.Path())
	return alias, dev, nil
}

// promptDefault writes prompt+default to out, reads a single line from
// reader, and returns the trimmed answer. Empty input yields the default.
func promptDefault(out io.Writer, reader *bufio.Reader, prompt, def string) (string, error) {
	fmt.Fprintf(out, "%s: ", prompt)
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	line = strings.TrimRight(line, "\r\n")
	if strings.TrimSpace(line) == "" {
		return def, nil
	}
	return line, nil
}

// promptIndex reads an integer in [1,n] from reader. Re-prompts up to a
// small number of times before giving up.
func promptIndex(out io.Writer, reader *bufio.Reader, n int) (int, error) {
	for attempts := 0; attempts < 5; attempts++ {
		fmt.Fprintf(out, "Pick one [1-%d]: ", n)
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return 0, err
		}
		line = strings.TrimSpace(line)
		idx, perr := strconv.Atoi(line)
		if perr != nil || idx < 1 || idx > n {
			fmt.Fprintf(out, "  please enter a number between 1 and %d\n", n)
			continue
		}
		return idx - 1, nil
	}
	return 0, fmt.Errorf("invalid selection")
}

// slugify converts a device's friendlyName ("RX-V583 FBE863") into an
// alias-friendly form ("rx-v583-fbe863"). Lowercases, replaces any non
// alnum run with a single "-", and trims leading/trailing dashes.
func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "device"
	}
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "device"
	}
	return out
}

// uniqueAlias returns base if it isn't taken in cfg.Devices, otherwise
// "base-2", "base-3", … until a free slot is found.
func uniqueAlias(base string, cfg *config.Config) string {
	if cfg == nil || cfg.Devices == nil {
		return base
	}
	if _, taken := cfg.Devices[base]; !taken {
		return base
	}
	for i := 2; i < 1000; i++ {
		cand := fmt.Sprintf("%s-%d", base, i)
		if _, taken := cfg.Devices[cand]; !taken {
			return cand
		}
	}
	return base // give up; caller will surface the collision
}

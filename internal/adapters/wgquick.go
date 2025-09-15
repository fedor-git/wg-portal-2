package adapters

import (
	"bytes"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"

	"github.com/fedor-git/wg-portal-2/internal/config"

	"github.com/fedor-git/wg-portal-2/internal"
	"github.com/fedor-git/wg-portal-2/internal/domain"
)

// WgQuickRepo implements higher level wg-quick like interactions like setting DNS, routing tables or interface hooks.
type WgQuickRepo struct {
	cfg                   *config.Config
	shellCmd              string
	resolvConfIfacePrefix string
}

// NewWgQuickRepo creates a new WgQuickRepo instance.
func NewWgQuickRepo(cfg *config.Config) *WgQuickRepo {
	return &WgQuickRepo{
		cfg:                   cfg,
		shellCmd:              "bash",
		resolvConfIfacePrefix: "tun.",
	}
}

// ExecuteInterfaceHook executes the given hook command.
// The hook command can contain the following placeholders:
//
//	%i: the interface identifier.
func (r *WgQuickRepo) ExecuteInterfaceHook(id domain.InterfaceIdentifier, hookCmd string) error {
	if hookCmd == "" {
		return nil
	}

	slog.Debug("executing interface hook", "interface", id, "hook", hookCmd)
	err := r.exec(hookCmd, id)
	if err != nil {
		return fmt.Errorf("failed to exec hook: %w", err)
	}

	return nil
}

// SetDNS sets the DNS settings for the given interface. It uses resolvconf to set the DNS settings.
func (r *WgQuickRepo) SetDNS(id domain.InterfaceIdentifier, dnsStr, dnsSearchStr string) error {
	if !r.cfg.Core.ManageDns {
		slog.Debug("DNS management is disabled in the config, skipping SetDNS.", "interface", id)
		return nil
	}

	if dnsStr == "" && dnsSearchStr == "" {
		return nil
	}

	dnsServers := internal.SliceString(dnsStr)
	dnsSearchDomains := internal.SliceString(dnsSearchStr)

	dnsCommand := "resolvconf -a %resPref%i -m 0 -x"
	dnsCommandInput := make([]string, 0, len(dnsServers)+len(dnsSearchDomains))

	for _, dnsServer := range dnsServers {
		dnsCommandInput = append(dnsCommandInput, fmt.Sprintf("nameserver %s", dnsServer))
	}
	for _, searchDomain := range dnsSearchDomains {
		dnsCommandInput = append(dnsCommandInput, fmt.Sprintf("search %s", searchDomain))
	}

	err := r.exec(dnsCommand, id, dnsCommandInput...)
	if err != nil {
		return fmt.Errorf(
			"failed to set dns settings (is resolvconf available?, for systemd create this symlink: ln -s /usr/bin/resolvectl /usr/local/bin/resolvconf): %w",
			err,
		)
	}

	return nil
}

// UnsetDNS unsets the DNS settings for the given interface. It uses resolvconf to unset the DNS settings.
func (r *WgQuickRepo) UnsetDNS(id domain.InterfaceIdentifier) error {
	if !r.cfg.Core.ManageDns {
		slog.Debug("DNS management is disabled in the config, skipping UnsetDNS.", "interface", id)
		return nil
	}

	dnsCommand := "resolvconf -d %resPref%i -f"

	err := r.exec(dnsCommand, id)
	if err != nil {
		return fmt.Errorf("failed to unset dns settings: %w", err)
	}

	return nil
}

func (r *WgQuickRepo) replaceCommandPlaceHolders(command string, interfaceId domain.InterfaceIdentifier) string {
	command = strings.ReplaceAll(command, "%resPref", r.resolvConfIfacePrefix)
	return strings.ReplaceAll(command, "%i", string(interfaceId))
}

func (r *WgQuickRepo) exec(command string, interfaceId domain.InterfaceIdentifier, stdin ...string) error {
	commandWithInterfaceName := r.replaceCommandPlaceHolders(command, interfaceId)
	cmd := exec.Command(r.shellCmd, "-ce", commandWithInterfaceName)
	if len(stdin) > 0 {
		b := &bytes.Buffer{}
		for _, ln := range stdin {
			if _, err := fmt.Fprint(b, ln); err != nil {
				return err
			}
		}
		cmd.Stdin = b
	}
	out, err := cmd.CombinedOutput() // execute and wait for output
	if err != nil {
		return fmt.Errorf("failed to exexute shell command %s: %w", commandWithInterfaceName, err)
	}
	slog.Debug("executed shell command",
		"command", commandWithInterfaceName,
		"output", string(out))
	return nil
}

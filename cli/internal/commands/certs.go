package commands

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/shipwick/shipwick/cli/internal/client"
	"github.com/shipwick/shipwick/cli/internal/ui"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// expiresSoon is how close to its end a certificate is pointed out in the
// list: nobody renews a supplied certificate but the person who supplied it.
const expiresSoon = 30 * 24 * time.Hour

// certCommands are the commands for certificates the operator supplies.
func (c *cli) certCommands() []*cobra.Command {
	cert := &cobra.Command{
		Use:   "cert",
		Short: "Serve a hostname with a certificate of your own",
		Long: `Serve a hostname with a certificate of your own.

Certificates are obtained and renewed by the server on its own; nothing here
is needed for that. This is for a hostname whose certificate comes from
somewhere else: a corporate authority, a wildcard bought for the whole domain.

A certificate belongs to the server, not to one application. Every hostname
it covers, of any application, is served with it; the server asks no
authority for those hostnames and does not wait for their DNS. Its key is
encrypted at rest like secrets are, and nobody reads it back. Shipwick does
not renew it: "cert ls" shows when it expires, and "cert set" replaces it.
Supplying and removing certificates needs admin; listing them needs read.`,
	}
	cert.AddCommand(c.certSetCommand(), c.certListCommand(), c.certRemoveCommand())
	return []*cobra.Command{cert}
}

func (c *cli) certSetCommand() *cobra.Command {
	var certFile, keyFile string
	cmd := &cobra.Command{
		Use:   "set <hostname>",
		Short: "Store a certificate and its key for a hostname, or replace them",
		Long: `Store a certificate and its key for a hostname, or replace the ones stored.

  shipwick cert set example.com --cert fullchain.pem --key privkey.pem
  shipwick cert set '*.example.com' --cert wildcard.pem --key wildcard.key

--cert is the chain in PEM: the certificate for the hostname first, then the
intermediates. --key is its private key in PEM, without a passphrase. The
server checks that they belong together, that the certificate covers the
hostname and that it has not expired, and refuses them otherwise.

A wildcard certificate is stored under the wildcard. It covers every name one
label below the domain, and lets "*.example.com" be used as a domain or alias
in deploy.yaml.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			hostname, err := certHostname(args[0])
			if err != nil {
				return err
			}
			if certFile == "" || keyFile == "" {
				return fmt.Errorf("both files are needed\n\nGive them as: shipwick cert set %s --cert fullchain.pem --key privkey.pem", shellQuote(hostname))
			}
			chain, err := readPEMFile("--cert", certFile)
			if err != nil {
				return err
			}
			key, err := readPEMFile("--key", keyFile)
			if err != nil {
				return err
			}

			cl, err := c.connect()
			if err != nil {
				return err
			}
			stored, err := cl.SetCertificate(cmd.Context(), hostname, chain, key)
			if err != nil {
				return err
			}
			c.ui.Success("Stored the certificate for %s", stored.Hostname)
			c.ui.Fields([][2]string{
				{"Issuer", stored.Issuer},
				{"Expires", expiry(stored.NotAfter, c.now())},
				{"Covers", strings.Join(stored.Subjects, ", ")},
			})
			c.ui.Println(c.ui.Styled(ui.Dim, "  The hostnames it covers are served with it from now on. It is not renewed for you: replace it before it expires with the same command."))
			return nil
		},
	}
	cmd.Flags().StringVar(&certFile, "cert", "", "the certificate chain, PEM: the hostname's certificate first, then the intermediates")
	cmd.Flags().StringVar(&keyFile, "key", "", "the private key, PEM, without a passphrase")
	return cmd
}

func (c *cli) certListCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List the certificates you supplied: issuer, expiry and what they cover",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cl, err := c.connect()
			if err != nil {
				return err
			}
			certificates, err := cl.Certificates(cmd.Context())
			if err != nil {
				return err
			}
			if len(certificates) == 0 {
				c.ui.Println("No certificates of your own on the server; it obtains one for every hostname itself. Supply one with: shipwick cert set example.com --cert fullchain.pem --key privkey.pem")
				return nil
			}

			now := c.now()
			rows := make([][]ui.Cell, 0, len(certificates))
			for _, cert := range certificates {
				expires := ui.C(expiry(cert.NotAfter, now))
				switch left := cert.NotAfter.Sub(now); {
				case left <= 0:
					expires.Style = ui.Red
				case left < expiresSoon:
					expires.Style = ui.Yellow
				}
				rows = append(rows, []ui.Cell{
					ui.C(cert.Hostname),
					ui.C(cert.Issuer),
					expires,
					ui.C(strings.Join(cert.Subjects, ", ")),
				})
			}
			c.ui.Table([]string{"HOSTNAME", "ISSUER", "EXPIRES", "COVERS"}, rows)
			return nil
		},
	}
}

func (c *cli) certRemoveCommand() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "rm <hostname>",
		Aliases: []string{"remove"},
		Short:   "Remove a certificate; the server obtains its own for those hostnames again",
		Long: `Remove the certificate stored under a hostname.

The hostnames it covered go back to certificates the server obtains itself,
which needs their DNS to point at the server. A wildcard hostname in a
deploy.yaml is no longer served unless the agent has
SHIPWICK_CLOUDFLARE_API_TOKEN.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			hostname, err := certHostname(args[0])
			if err != nil {
				return err
			}
			if !yes {
				if !isTerminal(c.in) {
					return errors.New("refusing to remove without confirmation; pass --yes")
				}
				c.ui.Printf("Remove the certificate for %s? The server will obtain its own for the hostnames it covers. [y/N] ", hostname)
				answer, _ := bufio.NewReader(c.in).ReadString('\n')
				if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
					return errors.New("cancelled")
				}
			}

			cl, err := c.connect()
			if err != nil {
				return err
			}
			if err := cl.DeleteCertificate(cmd.Context(), hostname); err != nil {
				if client.IsCode(err, api.CodeNotFound) {
					return fmt.Errorf("there is no certificate stored under %s\n\nList them with: shipwick cert ls", hostname)
				}
				return err
			}
			c.ui.Success("Removed the certificate for %s", hostname)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}

// certHostname normalizes the hostname a certificate is stored under the way
// a deploy.yaml's domain is normalized.
func certHostname(arg string) (string, error) {
	hostname := strings.ToLower(strings.TrimSpace(arg))
	if err := spec.ValidateHostname(hostname); err != nil {
		return "", fmt.Errorf("%w\n\nA hostname such as example.com, or a wildcard in quotes: '*.example.com'", err)
	}
	return hostname, nil
}

// shellQuote quotes a wildcard so that a suggested command can be pasted: an
// unquoted * is the shell's to expand.
func shellQuote(hostname string) string {
	if spec.IsWildcard(hostname) {
		return "'" + hostname + "'"
	}
	return hostname
}

// readPEMFile reads the file a flag names. Whether it holds what it should is
// the agent's to judge; the errors here never repeat the content.
func readPEMFile(flag, path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("%s: %w", flag, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s: %s is a directory, not a PEM file", flag, path)
	}
	if info.Size() > api.MaxCertificatePEMBytes {
		return "", fmt.Errorf("%s: %s is larger than %d KB; a certificate chain or a key in PEM is a few kilobytes", flag, path, api.MaxCertificatePEMBytes/1024)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("%s: %w", flag, err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return "", fmt.Errorf("%s: %s is empty", flag, path)
	}
	return string(data), nil
}

// expiry is a certificate's last day, with how far off it is once that
// matters.
func expiry(notAfter, now time.Time) string {
	day := notAfter.UTC().Format("2006-01-02")
	switch left := notAfter.Sub(now); {
	case left <= 0:
		return day + " (expired)"
	case left < 24*time.Hour:
		return day + " (today)"
	case left < expiresSoon:
		return fmt.Sprintf("%s (in %s)", day, plural(int(left/(24*time.Hour)), "day"))
	}
	return day
}

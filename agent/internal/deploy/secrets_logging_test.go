package deploy

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/shipwick/shipwick/pkg/spec"
)

const splunkDocument = `name: my-api
image: my-api:1.0
port: 8080
logging:
  driver: splunk
  options:
    splunk-url: https://hec.example.com:8088
    splunk-token: ${SPLUNK_TOKEN}
`

func TestDeployFillsTheSplunkTokenFromTheStoredSecrets(t *testing.T) {
	h := newHarness(t)
	h.secrets("SPLUNK_TOKEN", "hec-1")
	h.deployDocument(splunkDocument)

	if opts := h.rt.Spec(h.replica("my-api").ID).LogOptions; opts["splunk-token"] != "hec-1" || opts["splunk-url"] != "https://hec.example.com:8088" {
		t.Errorf("the container's logging options = %v", opts)
	}
	config := h.config("my-api")
	if len(config.Masked) != 0 || !strings.Contains(config.Document, "splunk-token: ${SPLUNK_TOKEN}") || strings.Contains(config.Document, "hec-1") {
		t.Errorf("masked = %v:\n%s", config.Masked, config.Document)
	}
	// The record holds the value, opened again when it is read.
	d, _ := h.store.GetDeployment(context.Background(), config.DeploymentID)
	if d.Spec.Logging.Options["splunk-token"] != "hec-1" {
		t.Errorf("stored = %+v", d.Spec.Logging)
	}
}

func TestDeployRefusesASplunkTokenNoSecretAnswers(t *testing.T) {
	h := newHarness(t)
	a, err := spec.Parse([]byte(splunkDocument))
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.engine.Deploy(context.Background(), a)
	var missing *MissingSecretsError
	if !errors.As(err, &missing) {
		t.Fatalf("err = %v, want MissingSecretsError", err)
	}
	fields := missing.Fields()
	if len(fields) != 1 || fields[0].Field != "logging.options.splunk-token" || fields[0].Expected != "shipwick secret set SPLUNK_TOKEN" {
		t.Errorf("fields = %+v", fields)
	}
	if !strings.Contains(err.Error(), "deploy.yaml refers to ${SPLUNK_TOKEN}") {
		t.Errorf("err = %v", err)
	}
}

func TestALiteralSplunkTokenIsMaskedAndItsMaskIsRefused(t *testing.T) {
	h := newHarness(t)
	h.deployDocument(strings.Replace(splunkDocument, "${SPLUNK_TOKEN}", "a-literal-token", 1))

	config := h.config("my-api")
	if !reflect.DeepEqual(config.Masked, []string{"logging.options.splunk-token"}) || strings.Contains(config.Document, "a-literal-token") {
		t.Errorf("masked = %v:\n%s", config.Masked, config.Document)
	}
	a, err := spec.Parse([]byte(config.Document))
	if err != nil {
		t.Fatalf("the document does not parse: %v\n%s", err, config.Document)
	}
	var masked *MaskedValuesError
	if _, err := h.engine.Deploy(context.Background(), a); !errors.As(err, &masked) || !reflect.DeepEqual(masked.Masked, config.Masked) {
		t.Errorf("deploying the document as handed out: err = %v, want a refusal of %v", err, config.Masked)
	}
	if err := h.engine.Validate(context.Background(), a); !errors.As(err, &masked) {
		t.Errorf("validate: err = %v", err)
	}
	// What the API hands out of the application never has the token.
	apps, _ := h.engine.Applications(context.Background())
	detail, err := h.engine.Application(context.Background(), apps[0].Name)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Spec.Logging.Options["splunk-token"] != spec.Mask {
		t.Errorf("the application's spec carries %q", detail.Spec.Logging.Options["splunk-token"])
	}
}

package lineagecontrollerwebhook

import (
	"context"
	"os"
	"testing"

	"sigs.k8s.io/yaml"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
)

func loadDefinition(t *testing.T, path string) *cozyv1alpha1.ApplicationDefinition {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	def := &cozyv1alpha1.ApplicationDefinition{}
	if err := yaml.Unmarshal(raw, def); err != nil {
		t.Fatal(err)
	}
	return def
}

// The platform Secrets a converted engine keeps must stay hidden on every
// write that drops their lineage labels, not only until an operator rewrites
// them: the webhook stamps them from these definitions each time.
func TestConvertedDefinitionsHidePlatformSecrets(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct{ rd, secret string }{
		{"rabbitmq", "rabbitmq-rmq-default-user"},
		{"mongodb", "mongodb-rmq-credentials"},
		{"mongodb", "mongodb-rmq-percona-server-mongodb-users"},
		{"mariadb", "mariadb-rmq-credentials"},
		{"clickhouse", "clickhouse-rmq-credentials"},
	} {
		def := loadDefinition(t, "../../packages/system/"+c.rd+"-rd/cozyrds/"+c.rd+".yaml")
		// An operator's rewrite carries its own labels, none of the lineage ones.
		if SecretTenantVisible(ctx, def, "rmq", "tenant-foo", c.secret, map[string]string{"app.kubernetes.io/name": "rmq"}) {
			t.Errorf("%s: the definition stamps %s tenant-visible", c.rd, c.secret)
		}
	}
}

func TestSecretTenantVisibleFollowsIncludeAndExclude(t *testing.T) {
	ctx := context.Background()
	sel := []*cozyv1alpha1.ApplicationDefinitionResourceSelector{{ResourceNames: []string{"{{ .kind }}-{{ .name }}-default-user"}}}
	def := &cozyv1alpha1.ApplicationDefinition{}
	def.Spec.Application.Kind = "RabbitMQ"
	def.Spec.Secrets.Include = sel
	if !SecretTenantVisible(ctx, def, "rmq", "tenant-foo", "rabbitmq-rmq-default-user", nil) {
		t.Fatal("an included Secret must be visible")
	}
	def.Spec.Secrets.Exclude = sel
	if SecretTenantVisible(ctx, def, "rmq", "tenant-foo", "rabbitmq-rmq-default-user", nil) {
		t.Fatal("exclude must win over include")
	}
	if SecretTenantVisible(ctx, nil, "rmq", "tenant-foo", "rabbitmq-rmq-default-user", nil) {
		t.Fatal("a kind with no definition stamps nothing visible")
	}
}

package kmspolicy_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/permaily-org/permaily/pkg/kmspolicy"
)

const (
	imageA = "e7bcd8dd5d91f78e48a5c686cc775272a59df69b89ec2dc28ccae651d8c0852e8af20ba9ac74632fbaef967bbe3b7f6a"
	imageB = "31e90c2d949962bffe22d3bca58a020e0b100e1fa51a3fc654eceae7233e76c0b8038c5822c3dca9f9971d5db67bbde7"
)

const admin = `{"Sid":"KeyAdministration","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::111122223333:root"},
"Action":["kms:Describe*","kms:Get*","kms:List*","kms:PutKeyPolicy","kms:EnableKeyRotation","kms:DisableKeyRotation",
"kms:TagResource","kms:UntagResource","kms:ScheduleKeyDeletion","kms:CancelKeyDeletion","kms:UpdateKeyDescription",
"kms:CreateAlias","kms:DeleteAlias","kms:UpdateAlias"],"Resource":"*"}`

func enclave(condition string) string {
	return `{"Sid":"EnclaveDataKeys","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::111122223333:role/host"},
"Action":["kms:GenerateDataKey","kms:Decrypt"],"Resource":"*","Condition":` + condition + `}`
}

func policy(statements ...string) []byte {
	return []byte(`{"Version":"2012-10-17","Statement":[` + strings.Join(statements, ",") + `]}`)
}

func pinned(images ...string) string {
	return `{"StringEqualsIgnoreCase":{"kms:RecipientAttestation:ImageSha384":["` + strings.Join(images, `","`) + `"]}}`
}

func TestAnalyzeProductionShape(t *testing.T) {
	r, err := kmspolicy.Analyze(policy(admin, enclave(pinned(imageA))))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.Images, []string{imageA}) || len(r.Problems) != 0 || !r.AdminCanChangePolicy {
		t.Fatalf("unexpected report: %+v", r)
	}
}

func TestAnalyzeImages(t *testing.T) {
	cases := []struct {
		name   string
		policy []byte
		want   []string
	}{
		{"two images, sorted and deduplicated", policy(admin, enclave(pinned(imageA, imageB, imageA))), []string{imageB, imageA}},
		{"uppercase hash and a single value", policy(enclave(`{"StringEquals":{"kms:RecipientAttestation:PCR0":"` + strings.ToUpper(imageA) + `"}}`)), []string{imageA}},
		{"extra conditions only narrow", policy(enclave(`{"StringEqualsIgnoreCase":{"kms:RecipientAttestation:ImageSha384":"` + imageA + `"},"Bool":{"aws:SecureTransport":true}}`)), []string{imageA}},
		{"administration only", policy(admin), nil},
		{"one statement, not a list", []byte(`{"Version":"2012-10-17","Statement":` + enclave(pinned(imageB)) + `}`), []string{imageB}},
		{"action as text", policy(`{"Effect":"Allow","Principal":"*","Action":"kms:Decrypt","Resource":"*","Condition":` + pinned(imageA) + `}`), []string{imageA}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := kmspolicy.Analyze(c.policy)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(r.Images, c.want) || len(r.Problems) != 0 {
				t.Fatalf("images %v problems %v, want images %v and no problems", r.Images, r.Problems, c.want)
			}
		})
	}
}

// Each of these policies lets someone use the key without proving an image.
func TestAnalyzeProblems(t *testing.T) {
	stmt := func(action, extra string) string {
		return `{"Sid":"X","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::111122223333:root"},"Action":` + action + `,"Resource":"*"` + extra + `}`
	}
	cases := []struct {
		name   string
		policy []byte
		want   string
	}{
		{"decrypt without condition", policy(admin, stmt(`"kms:Decrypt"`, "")), "without requiring an enclave image"},
		{"every action", policy(stmt(`"kms:*"`, "")), "kms:*"},
		{"star", policy(stmt(`"*"`, "")), "without requiring an enclave image"},
		{"grants", policy(stmt(`["kms:CreateGrant"]`, "")), "kms:CreateGrant"},
		{"encrypt lets the operator plant a key", policy(stmt(`"kms:Encrypt"`, "")), "kms:Encrypt"},
		{"re-encrypt to another key", policy(stmt(`"kms:ReEncrypt*"`, "")), "kms:ReEncrypt*"},
		{"replica with its own policy", policy(stmt(`"kms:ReplicateKey"`, "")), "kms:ReplicateKey"},
		{"wildcard that is not a known safe one", policy(stmt(`"kms:G*"`, "")), "kms:G*"},
		{"unrelated condition", policy(stmt(`"kms:Decrypt"`, `,"Condition":{"StringEquals":{"aws:PrincipalTag/team":"ops"}}`)), "without requiring an enclave image"},
		{"pattern match", policy(enclave(`{"StringLike":{"kms:RecipientAttestation:ImageSha384":"*"}}`)), "does not compare the whole value"},
		{"if exists", policy(enclave(`{"StringEqualsIfExists":{"kms:RecipientAttestation:ImageSha384":"` + imageA + `"}}`)), "does not compare the whole value"},
		{"negated", policy(enclave(`{"StringNotEquals":{"kms:RecipientAttestation:ImageSha384":"` + imageA + `"}}`)), "does not compare the whole value"},
		{"empty list", policy(enclave(`{"StringEquals":{"kms:RecipientAttestation:ImageSha384":[]}}`)), "empty image list"},
		{"not a hash", policy(enclave(`{"StringEquals":{"kms:RecipientAttestation:ImageSha384":"abc"}}`)), "not a SHA-384 image hash"},
		{"hash with a wrong character", policy(enclave(`{"StringEquals":{"kms:RecipientAttestation:ImageSha384":"` + strings.Repeat("g", 96) + `"}}`)), "not a SHA-384 image hash"},
		{"image is not text", policy(enclave(`{"StringEquals":{"kms:RecipientAttestation:ImageSha384":7}}`)), "not text"},
		{"not action", policy(`{"Effect":"Allow","Principal":"*","NotAction":"kms:Decrypt","Resource":"*"}`), "NotAction"},
		{"not principal", policy(`{"Effect":"Allow","NotPrincipal":{"AWS":"x"},"Action":"kms:DescribeKey","Resource":"*"}`), "NotAction"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := kmspolicy.Analyze(c.policy)
			if err != nil {
				t.Fatal(err)
			}
			if len(r.Problems) != 1 || !strings.Contains(r.Problems[0], c.want) {
				t.Fatalf("problems %q, want one containing %q", r.Problems, c.want)
			}
			if len(r.Images) != 0 {
				t.Fatalf("a statement with a problem contributed images: %v", r.Images)
			}
		})
	}
}

func TestAnalyzePolicyLock(t *testing.T) {
	deny := func(principal, extra string) string {
		return `{"Sid":"Lock","Effect":"Deny","Principal":` + principal + `,"Action":"kms:PutKeyPolicy","Resource":"*"` + extra + `}`
	}
	cases := []struct {
		name   string
		policy []byte
		want   bool
	}{
		{"administrators can change it", policy(admin), true},
		{"through a wildcard", policy(`{"Effect":"Allow","Principal":"*","Action":"kms:Put*","Resource":"*","Condition":` + pinned(imageA) + `}`), true},
		{"nobody is allowed", policy(enclave(pinned(imageA))), false},
		{"denied for everyone", policy(admin, deny(`"*"`, "")), false},
		{"denied for everyone, AWS form", policy(admin, deny(`{"AWS":"*"}`, "")), false},
		{"denied for one principal only", policy(admin, deny(`{"AWS":"arn:aws:iam::111122223333:user/a"}`, "")), true},
		{"denied with a condition", policy(admin, deny(`"*"`, `,"Condition":{"Bool":{"aws:MultiFactorAuthPresent":"false"}}`)), true},
		{"denied by service principal", policy(admin, deny(`{"Service":"*"}`, "")), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := kmspolicy.Analyze(c.policy)
			if err != nil {
				t.Fatal(err)
			}
			if r.AdminCanChangePolicy != c.want {
				t.Fatalf("AdminCanChangePolicy = %v, want %v", r.AdminCanChangePolicy, c.want)
			}
		})
	}
}

// The policy of a release key, as deploy/tofu creates it.
func releasePolicy(image string) []byte {
	return policy(
		`{"Sid":"Read","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::111122223333:root"},"Action":["kms:DescribeKey","kms:GetKeyPolicy","kms:GetKeyRotationStatus","kms:ListGrants","kms:ListResourceTags","kms:ListKeyPolicies","kms:ScheduleKeyDeletion","kms:CancelKeyDeletion"],"Resource":"*"}`,
		`{"Sid":"AnyoneLocks","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::111122223333:root"},"Action":"kms:GenerateDataKey","Resource":"*"}`,
		`{"Sid":"OnlyThisImageOpens","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::111122223333:root"},"Action":"kms:Decrypt","Resource":"*","Condition":`+pinned(image)+`}`,
	)
}

var goodKey = kmspolicy.Key{Origin: "AWS_KMS", Spec: "SYMMETRIC_DEFAULT", Usage: "ENCRYPT_DECRYPT", Manager: "CUSTOMER", State: "Enabled"}

func TestSealed(t *testing.T) {
	rep, err := kmspolicy.Sealed(releasePolicy(imageA), 0, false, goodKey, strings.ToUpper(imageA))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rep.Images, []string{imageA}) {
		t.Fatalf("report %+v", rep)
	}
	// A key scheduled for deletion is still sealed: it only stops working.
	pending := goodKey
	pending.State = "PendingDeletion"
	if _, err := kmspolicy.Sealed(releasePolicy(imageA), 0, false, pending, imageA); err != nil {
		t.Fatal(err)
	}

	key := func(change func(k *kmspolicy.Key)) kmspolicy.Key {
		k := goodKey
		change(&k)
		return k
	}
	cases := []struct {
		name      string
		policy    []byte
		grants    int
		truncated bool
		key       kmspolicy.Key
		image     string
		want      string
	}{
		{"the policy can be replaced", policy(admin, enclave(pinned(imageA))), 0, false, goodKey, imageA, "can be replaced"},
		{"another image", releasePolicy(imageB), 0, false, goodKey, imageA, "not released to this image"},
		{"two images", policy(enclave(pinned(imageA, imageB))), 0, false, goodKey, imageA, "released to other images: " + imageB},
		{"no image at all", policy(`{"Effect":"Allow","Principal":"*","Action":"kms:DescribeKey","Resource":"*"}`), 0, false, goodKey, imageA, "no image can use the key"},
		{"someone decrypts without an image", policy(enclave(pinned(imageA)), `{"Sid":"Door","Effect":"Allow","Principal":"*","Action":"kms:Decrypt","Resource":"*"}`), 0, false, goodKey, imageA, "statement Door"},
		{"re-encryption without an image", policy(enclave(pinned(imageA)), `{"Sid":"Door","Effect":"Allow","Principal":"*","Action":["kms:Encrypt","kms:ReEncryptFrom"],"Resource":"*"}`), 0, false, goodKey, imageA, "allows kms:ReEncryptFrom without"},
		{"data key pairs without an image", policy(enclave(pinned(imageA)), `{"Sid":"Door","Effect":"Allow","Principal":"*","Action":"kms:GenerateDataKeyPair","Resource":"*"}`), 0, false, goodKey, imageA, "kms:GenerateDataKeyPair"},
		{"grants can be created", policy(enclave(pinned(imageA)), `{"Sid":"Door","Effect":"Allow","Principal":"*","Action":"kms:CreateGrant","Resource":"*"}`), 0, false, goodKey, imageA, "kms:CreateGrant"},
		{"a grant exists", releasePolicy(imageA), 1, false, goodKey, imageA, "1 grant(s)"},
		{"more grants than were listed", releasePolicy(imageA), 0, true, goodKey, imageA, "grant(s)"},
		{"imported key material", releasePolicy(imageA), 0, false, key(func(k *kmspolicy.Key) { k.Origin = "EXTERNAL" }), imageA, `origin "EXTERNAL"`},
		{"an external key store", releasePolicy(imageA), 0, false, key(func(k *kmspolicy.Key) { k.Origin = "EXTERNAL_KEY_STORE" }), imageA, "outside KMS"},
		{"a custom key store", releasePolicy(imageA), 0, false, key(func(k *kmspolicy.Key) { k.CustomKeyStore = "cks-1" }), imageA, "outside KMS"},
		{"a multi-Region key", releasePolicy(imageA), 0, false, key(func(k *kmspolicy.Key) { k.MultiRegion = true }), imageA, "multi-Region"},
		{"an asymmetric key", releasePolicy(imageA), 0, false, key(func(k *kmspolicy.Key) { k.Spec = "RSA_4096" }), imageA, "not a symmetric encryption key"},
		{"a signing key", releasePolicy(imageA), 0, false, key(func(k *kmspolicy.Key) { k.Usage = "GENERATE_VERIFY_MAC" }), imageA, "not a symmetric encryption key"},
		{"a key AWS manages", releasePolicy(imageA), 0, false, key(func(k *kmspolicy.Key) { k.Manager = "AWS" }), imageA, "not a symmetric encryption key"},
		{"no facts about the key", releasePolicy(imageA), 0, false, kmspolicy.Key{}, imageA, "outside KMS"},
		{"an image that is not a hash", releasePolicy(imageA), 0, false, goodKey, "abc", "not a SHA-384 hash"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := kmspolicy.Sealed(c.policy, c.grants, c.truncated, c.key, c.image)
			if !errors.Is(err, kmspolicy.ErrNotSealed) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want ErrNotSealed containing %q", err, c.want)
			}
		})
	}
	if _, err := kmspolicy.Sealed([]byte("nope"), 0, false, goodKey, imageA); !errors.Is(err, kmspolicy.ErrMalformed) {
		t.Fatalf("got %v", err)
	}
}

func TestAnalyzeMalformed(t *testing.T) {
	for name, p := range map[string]string{
		"not json":              `nope`,
		"empty":                 ``,
		"no statements":         `{"Version":"2012-10-17","Statement":[]}`,
		"unknown field":         `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"kms:DescribeKey","Surprise":1}]}`,
		"unknown top":           `{"Version":"2012-10-17","Extra":1,"Statement":[{"Effect":"Allow","Action":"kms:DescribeKey"}]}`,
		"unknown effect":        `{"Statement":[{"Effect":"Maybe","Action":"kms:DescribeKey"}]}`,
		"no actions":            `{"Statement":[{"Effect":"Allow","Principal":"*"}]}`,
		"trailing data":         `{"Statement":[{"Effect":"Allow","Action":"kms:DescribeKey"}]} {}`,
		"action is number":      `{"Statement":[{"Effect":"Allow","Action":7}]}`,
		"actions hold a number": `{"Statement":[{"Effect":"Allow","Action":["kms:DescribeKey",7]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := kmspolicy.Analyze([]byte(p)); !errors.Is(err, kmspolicy.ErrMalformed) {
				t.Fatalf("got %v, want ErrMalformed", err)
			}
		})
	}
}

func FuzzAnalyze(f *testing.F) {
	f.Add(policy(admin, enclave(pinned(imageA))))
	f.Add([]byte(`{"Statement":{"Effect":"Deny","Principal":"*","Action":"*"}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		r, err := kmspolicy.Analyze(data)
		if err != nil {
			return
		}
		for _, img := range r.Images {
			if len(img) != 96 || strings.ToLower(img) != img {
				t.Fatalf("image %q is not a lowercase SHA-384 hash", img)
			}
		}
	})
}

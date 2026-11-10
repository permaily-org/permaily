package main

import (
	"bytes"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/pkg/attestation"
)

const (
	imageA = "e7bcd8dd5d91f78e48a5c686cc775272a59df69b89ec2dc28ccae651d8c0852e8af20ba9ac74632fbaef967bbe3b7f6a"
	imageB = "31e90c2d949962bffe22d3bca58a020e0b100e1fa51a3fc654eceae7233e76c0b8038c5822c3dca9f9971d5db67bbde7"
	admin  = `{"Sid":"KeyAdministration","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::111122223333:root"},"Action":["kms:Describe*","kms:Get*","kms:List*","kms:PutKeyPolicy"],"Resource":"*"}`
)

func keyPolicy(images ...string) string {
	stmts := []string{admin}
	if len(images) > 0 {
		stmts = append(stmts, `{"Sid":"EnclaveDataKeys","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::111122223333:role/host"},"Action":["kms:GenerateDataKey","kms:Decrypt"],"Resource":"*","Condition":{"StringEqualsIgnoreCase":{"kms:RecipientAttestation:ImageSha384":["`+strings.Join(images, `","`)+`"]}}}`)
	}
	return `{"Version":"2012-10-17","Statement":[` + strings.Join(stmts, ",") + `]}`
}

func TestCheckKMS(t *testing.T) {
	now := time.Date(2026, 11, 10, 4, 0, 0, 0, time.UTC)
	status := func(policy string) *attestation.KMSStatus {
		return &attestation.KMSStatus{KeyARN: "arn:aws:kms:us-east-2:111122223333:key/k", Policy: policy, FetchedAt: now.Add(-time.Minute)}
	}

	t.Run("only the verified image", func(t *testing.T) {
		got, err := checkKMS(status(keyPolicy(imageA)), strings.ToUpper(imageA), nil, now)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got.Images, []string{imageA}) || !got.AdministratorsCanChangePolicy || len(got.PolicySHA256) != 64 || got.KeyARN == "" {
			t.Fatalf("result %+v", got)
		}
	})
	t.Run("no image at all", func(t *testing.T) {
		if got, err := checkKMS(status(keyPolicy()), imageA, nil, now); err != nil || len(got.Images) != 0 {
			t.Fatalf("got %+v, %v", got, err)
		}
	})
	t.Run("previous release accepted during an upgrade", func(t *testing.T) {
		if _, err := checkKMS(status(keyPolicy(imageA, imageB)), imageA, splitList(" "+strings.ToUpper(imageB)+" ,"), now); err != nil {
			t.Fatal(err)
		}
	})

	fails := []struct {
		name string
		k    *attestation.KMSStatus
		want string
	}{
		{"another image can read the mail", status(keyPolicy(imageA, imageB)), imageB},
		{"only another image", status(keyPolicy(imageB)), "did not verify"},
		{"a statement without the image check", status(`{"Statement":[` + admin + `,{"Effect":"Allow","Principal":"*","Action":"kms:Decrypt","Resource":"*"}]}`), "without requiring an enclave image"},
		{"a grant exists", func() *attestation.KMSStatus { k := status(keyPolicy(imageA)); k.Grants = 1; return k }(), "1 grant"},
		{"more grants than listed", func() *attestation.KMSStatus { k := status(keyPolicy(imageA)); k.GrantsTruncated = true; return k }(), "grant"},
		{"old reading", func() *attestation.KMSStatus {
			k := status(keyPolicy(imageA))
			k.FetchedAt = now.Add(-time.Hour)
			return k
		}(), "not within"},
		{"reading from the future", func() *attestation.KMSStatus {
			k := status(keyPolicy(imageA))
			k.FetchedAt = now.Add(time.Hour)
			return k
		}(), "not within"},
		{"policy is not JSON", status("nope"), "malformed"},
		{"no policy", status(""), "malformed"},
	}
	for _, c := range fails {
		t.Run(c.name, func(t *testing.T) {
			got, err := checkKMS(c.k, imageA, nil, now)
			if err == nil || got != nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %+v, %v; want an error containing %q", got, err, c.want)
			}
		})
	}
}

func TestPrintKMS(t *testing.T) {
	at := time.Date(2026, 11, 10, 4, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		r    hostResult
		want []string
		not  string
	}{
		{"skipped", hostResult{KMSSkipped: "not checked (-skip-kms-policy)"}, []string{"KMS key policy: not checked"}, "releases"},
		{"only this image", hostResult{KMS: &kmsResult{KeyARN: "arn:k", Images: []string{imageA}, PolicySHA256: "ab", ReadAt: at, AdministratorsCanChangePolicy: true}},
			[]string{"only to this image", "no grants", "sha256 ab", "2026-11-10T04:00:00Z", "administrators can replace"}, ""},
		{"locked policy", hostResult{KMS: &kmsResult{Images: []string{imageA}, ReadAt: at}}, []string{"only to this image"}, "administrators"},
		{"no image", hostResult{KMS: &kmsResult{ReadAt: at}}, []string{"to no image"}, "only to this image"},
		{"this and an accepted one", hostResult{KMS: &kmsResult{Images: []string{imageB, imageA}, ReadAt: at}}, []string{"this image and 1 more you accepted"}, "only to"},
		{"only an accepted one", hostResult{KMS: &kmsResult{Images: []string{imageB}, ReadAt: at}}, []string{"not to this one"}, "only to"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var b bytes.Buffer
			printKMS(&b, c.r, imageA)
			for _, w := range c.want {
				if !strings.Contains(b.String(), w) {
					t.Fatalf("output %q lacks %q", b.String(), w)
				}
			}
			if c.not != "" && strings.Contains(b.String(), c.not) {
				t.Fatalf("output %q contains %q", b.String(), c.not)
			}
		})
	}
}

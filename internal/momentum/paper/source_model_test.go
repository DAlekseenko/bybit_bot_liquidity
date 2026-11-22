package paper

import (
	"context"
	"encoding/json"
	"testing"

	"bybit_bot_liquidity/internal/momentum/config"
	"bybit_bot_liquidity/internal/momentum/paperwire"
)

// TestSourceAcceptsOpaqueModelIDs проверяет независимость публичного источника от модели.
func TestSourceAcceptsOpaqueModelIDs(t *testing.T) {
	r, _ := sourceFixture(t, "ALT123USDT")
	_, profileHash, err := paperwire.EncodeProfile(r.Profile)
	if err != nil {
		t.Fatal(err)
	}
	for _, modelID := range []string{"roc30", "opaque-v2", profileHash} {
		// Проверяет чтение и pure validation с исходным идентификатором.
		t.Run(modelID, func(t *testing.T) {
			r, fake := sourceFixture(t, "ALT123USDT")
			values := r.Run.Config.Values()
			values.ModelID = modelID
			encoded, err := json.Marshal(values)
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := config.Parse(encoded)
			if err != nil {
				t.Fatal(err)
			}
			r.Run.Config = cfg
			_, originalProfileHash, err := paperwire.EncodeProfile(r.Profile)
			if err != nil {
				t.Fatal(err)
			}
			originalModel := cfg.Values().ModelID
			o, err := ReadCycleObservation(context.Background(), r)
			if err != nil {
				t.Fatalf("read public observation: %v", err)
			}
			if fake.calls == 0 {
				t.Fatal("public fake was not called")
			}
			r.Transport = nil
			if _, err = ValidateCycleObservation(r, o); err != nil {
				t.Fatalf("validate saved observation: %v", err)
			}
			_, currentProfileHash, err := paperwire.EncodeProfile(r.Profile)
			if err != nil || currentProfileHash != originalProfileHash || r.Run.Config.Values().ModelID != originalModel {
				t.Fatal("source validation mutated profile or model ID", err)
			}
		})
	}
}

// TestSourceRejectsNonPaperConfigBeforeRPC сохраняет режимный guard источника.
func TestSourceRejectsNonPaperConfigBeforeRPC(t *testing.T) {
	r, fake := sourceFixture(t, "ALT123USDT")
	values := r.Run.Config.Values()
	values.Mode = "backtest"
	values.DataSource = config.DataSource{Kind: "frozen_dataset", Environment: "mainnet", DatasetID: "dataset-v1", ManifestSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	encoded, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	r.Run.Config, err = config.Parse(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if err = sourceRequest(r); err == nil || fake.calls != 0 {
		t.Fatal("non-paper config reached public RPC", err, fake.calls)
	}
}

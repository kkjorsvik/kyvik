package types

import "testing"

func TestNormalizeContextBudget_ZeroGetsDefaults(t *testing.T) {
	cb := NormalizeContextBudget(ContextBudget{})

	if cb.MaxTotalTokens != DefaultContextMaxTotalTokens {
		t.Errorf("MaxTotalTokens = %d, want %d", cb.MaxTotalTokens, DefaultContextMaxTotalTokens)
	}
	if cb.SoulIdentityPct != DefaultContextSoulIdentityPct {
		t.Errorf("SoulIdentityPct = %d, want %d", cb.SoulIdentityPct, DefaultContextSoulIdentityPct)
	}
	if cb.SkillsPct != DefaultContextSkillsPct {
		t.Errorf("SkillsPct = %d, want %d", cb.SkillsPct, DefaultContextSkillsPct)
	}
	if cb.MemoriesPct != DefaultContextMemoriesPct {
		t.Errorf("MemoriesPct = %d, want %d", cb.MemoriesPct, DefaultContextMemoriesPct)
	}
	if cb.HistoryPct != DefaultContextHistoryPct {
		t.Errorf("HistoryPct = %d, want %d", cb.HistoryPct, DefaultContextHistoryPct)
	}
}

func TestNormalizeContextBudget_CustomValuesPreserved(t *testing.T) {
	input := ContextBudget{
		MaxTotalTokens:  4000,
		SoulIdentityPct: 20,
		SkillsPct:       5,
		MemoriesPct:     30,
		HistoryPct:      45,
	}
	cb := NormalizeContextBudget(input)

	if cb.MaxTotalTokens != 4000 {
		t.Errorf("MaxTotalTokens = %d, want 4000", cb.MaxTotalTokens)
	}
	if cb.SoulIdentityPct != 20 {
		t.Errorf("SoulIdentityPct = %d, want 20", cb.SoulIdentityPct)
	}
	if cb.SkillsPct != 5 {
		t.Errorf("SkillsPct = %d, want 5", cb.SkillsPct)
	}
	if cb.MemoriesPct != 30 {
		t.Errorf("MemoriesPct = %d, want 30", cb.MemoriesPct)
	}
	if cb.HistoryPct != 45 {
		t.Errorf("HistoryPct = %d, want 45", cb.HistoryPct)
	}
}

func TestNormalizeContextBudget_NegativeValuesDefaulted(t *testing.T) {
	cb := NormalizeContextBudget(ContextBudget{
		MaxTotalTokens:  -1,
		SoulIdentityPct: -5,
		SkillsPct:       -10,
		MemoriesPct:     -3,
		HistoryPct:      -1,
	})

	if cb.MaxTotalTokens != DefaultContextMaxTotalTokens {
		t.Errorf("MaxTotalTokens = %d, want %d", cb.MaxTotalTokens, DefaultContextMaxTotalTokens)
	}
	if cb.SoulIdentityPct != DefaultContextSoulIdentityPct {
		t.Errorf("SoulIdentityPct = %d, want %d", cb.SoulIdentityPct, DefaultContextSoulIdentityPct)
	}
	if cb.SkillsPct != DefaultContextSkillsPct {
		t.Errorf("SkillsPct = %d, want %d", cb.SkillsPct, DefaultContextSkillsPct)
	}
	if cb.MemoriesPct != DefaultContextMemoriesPct {
		t.Errorf("MemoriesPct = %d, want %d", cb.MemoriesPct, DefaultContextMemoriesPct)
	}
	if cb.HistoryPct != DefaultContextHistoryPct {
		t.Errorf("HistoryPct = %d, want %d", cb.HistoryPct, DefaultContextHistoryPct)
	}
}

func TestNormalizeContextBudget_PartialDefaults(t *testing.T) {
	cb := NormalizeContextBudget(ContextBudget{
		MaxTotalTokens: 16000,
		// rest are zero — should get defaults
	})

	if cb.MaxTotalTokens != 16000 {
		t.Errorf("MaxTotalTokens = %d, want 16000", cb.MaxTotalTokens)
	}
	if cb.SoulIdentityPct != DefaultContextSoulIdentityPct {
		t.Errorf("SoulIdentityPct = %d, want %d", cb.SoulIdentityPct, DefaultContextSoulIdentityPct)
	}
	if cb.SkillsPct != DefaultContextSkillsPct {
		t.Errorf("SkillsPct = %d, want %d", cb.SkillsPct, DefaultContextSkillsPct)
	}
	if cb.MemoriesPct != DefaultContextMemoriesPct {
		t.Errorf("MemoriesPct = %d, want %d", cb.MemoriesPct, DefaultContextMemoriesPct)
	}
	if cb.HistoryPct != DefaultContextHistoryPct {
		t.Errorf("HistoryPct = %d, want %d", cb.HistoryPct, DefaultContextHistoryPct)
	}
}

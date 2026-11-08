package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
)

// Сервис рекомендаций команды ML. Он отдаёт кандидатов с признаками, а ранжируем мы сами.
const recsURL = "http://127.0.0.1:9090/candidates?product=%d"

type candidate struct {
	ID       int       `json:"id"`
	Score    float64   `json:"score"`
	Features []float64 `json:"features"`
}

type recsResult struct {
	cands []candidate
	err   error
}

func fetchCandidates(product int) ([]candidate, error) {
	resp, err := http.Get(fmt.Sprintf(recsURL, product))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var out struct {
		Candidates []candidate `json:"candidates"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Candidates, nil
}

// topN возвращает id лучших кандидатов, при равном счёте меньший id первым.
func topN(cands []candidate, n int) []int {
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].Score != cands[j].Score {
			return cands[i].Score > cands[j].Score
		}
		return cands[i].ID < cands[j].ID
	})
	ids := make([]int, 0, n)
	for _, c := range cands[:min(n, len(cands))] {
		ids = append(ids, c.ID)
	}
	return ids
}

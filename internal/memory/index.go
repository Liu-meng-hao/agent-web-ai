package memory

import (
	"math"
	"sort"
	"strings"
	"unicode"
)

// 索引参数：BM25 常用取值，事实文本短，b 取 0.75 足够。
const (
	bm25K1 = 1.2
	bm25B  = 0.75
)

type posting struct {
	doc int
	tf  int
}

// Index 是内存中的 BM25 倒排索引。
//
// 事实量级在 10³~10⁴，写入时整体重建即可，不做增量维护，
// 也就没有"快照过期"这一类问题。
type Index struct {
	docs     []*Fact
	lens     []int
	postings map[string][]posting
	avgDL    float64
}

// BuildIndex 从活跃事实构建索引。
func BuildIndex(facts []*Fact) *Index {
	idx := &Index{docs: facts, postings: map[string][]posting{}}
	idx.lens = make([]int, len(facts))
	total := 0
	for i, f := range facts {
		tokens := Tokenize(f.Key + " " + f.Text + " " + strings.Join(f.Tags, " "))
		idx.lens[i] = len(tokens)
		total += len(tokens)
		counts := map[string]int{}
		for _, t := range tokens {
			counts[t]++
		}
		for t, tf := range counts {
			idx.postings[t] = append(idx.postings[t], posting{doc: i, tf: tf})
		}
	}
	if len(facts) > 0 {
		idx.avgDL = float64(total) / float64(len(facts))
	}
	return idx
}

// Scored 是带分数的事实。
type Scored struct {
	Fact *Fact
	BM25 float64
}

// Search 返回按 BM25 分数排序的前 k 条事实。
func (idx *Index) Search(query string, k int) []Scored {
	if idx == nil || len(idx.docs) == 0 {
		return nil
	}
	terms := dedupe(Tokenize(query))
	if len(terms) == 0 {
		return nil
	}
	scores := make([]float64, len(idx.docs))
	n := float64(len(idx.docs))
	for _, term := range terms {
		plist := idx.postings[term]
		if len(plist) == 0 {
			continue
		}
		df := float64(len(plist))
		idf := math.Log(1 + (n-df+0.5)/(df+0.5))
		for _, p := range plist {
			dl := float64(idx.lens[p.doc])
			tf := float64(p.tf)
			denom := tf + bm25K1*(1-bm25B+bm25B*dl/idx.avgDL)
			scores[p.doc] += idf * tf * (bm25K1 + 1) / denom
		}
	}

	out := make([]Scored, 0, len(idx.docs))
	for i, s := range scores {
		if s > 0 {
			out = append(out, Scored{Fact: idx.docs[i], BM25: s})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].BM25 == out[j].BM25 {
			return out[i].Fact.UpdatedAt.After(out[j].Fact.UpdatedAt)
		}
		return out[i].BM25 > out[j].BM25
	})
	if k > 0 && len(out) > k {
		out = out[:k]
	}
	return out
}

// Tokenize 把文本切成检索词：ASCII 按词，CJK 按双字 bigram。
//
// 中文没有空格，bigram 能在不引入分词器的前提下拿到可用的召回率。
func Tokenize(s string) []string {
	s = strings.ToLower(s)
	var out []string
	var word []rune
	var cjk []rune

	flushWord := func() {
		if len(word) > 0 {
			out = append(out, string(word))
			word = word[:0]
		}
	}
	flushCJK := func() {
		switch len(cjk) {
		case 0:
		case 1:
			out = append(out, string(cjk))
		default:
			for i := 0; i+1 < len(cjk); i++ {
				out = append(out, string(cjk[i:i+2]))
			}
		}
		cjk = cjk[:0]
	}

	for _, r := range s {
		switch {
		case unicode.Is(unicode.Han, r):
			flushWord()
			cjk = append(cjk, r)
		case r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'):
			flushCJK()
			word = append(word, r)
		default:
			flushWord()
			flushCJK()
		}
	}
	flushWord()
	flushCJK()
	return out
}

func dedupe(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := in[:0]
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// jaccard 用于近重复合并。
func jaccard(a, b []string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	set := make(map[string]bool, len(a))
	for _, s := range a {
		set[s] = true
	}
	seenB := make(map[string]bool, len(b))
	inter := 0
	uni := len(set)
	for _, s := range b {
		if seenB[s] {
			continue
		}
		seenB[s] = true
		if set[s] {
			inter++
		} else {
			uni++
		}
	}
	if uni == 0 {
		return 0
	}
	return float64(inter) / float64(uni)
}

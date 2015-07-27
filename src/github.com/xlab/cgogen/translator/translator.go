package translator

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"go/token"

	"github.com/cznic/c/internal/cc"
	"github.com/cznic/c/internal/xc"
)

type Translator struct {
	out         io.Writer
	rules       Rules
	compiledRxs map[RuleAction]RxMap
}

type RxMap map[RuleTarget][]Rx

type Rx struct {
	From *regexp.Regexp
	To   []byte
	//
	Transform RuleTransform
}

func New(rules Rules, out io.Writer) (*Translator, error) {
	t := &Translator{
		rules:       rules,
		out:         out,
		compiledRxs: make(map[RuleAction]RxMap),
	}
	for _, action := range ruleActions {
		if rxMap, err := getRuleActionRxs(rules, action); err != nil {
			return nil, err
		} else {
			t.compiledRxs[action] = rxMap
		}
	}
	return t, nil
}

func getRuleActionRxs(rules Rules, action RuleAction) (RxMap, error) {
	rxMap := make(RxMap, len(rules))
	for target, specs := range rules {
		for _, spec := range specs {
			if spec.Action == ActionNone {
				spec.Action = ActionReplace
				spec.To = "${_src}"
				if len(spec.From) == 0 {
					spec.From = "(?P<_src>.*)"
				} else {
					spec.From = fmt.Sprintf("(?P<_src>%s)", spec.From)
				}
			} else if len(spec.From) == 0 {
				spec.From = "(.*)"
			}
			if spec.Action != action {
				continue
			}
			rxFrom, err := regexp.Compile(spec.From)
			if err != nil {
				return nil, errors.New(fmt.Sprintf("translator: %s rules: invalid regexp %s", target, spec.From))
			}
			rx := Rx{From: rxFrom, To: []byte(spec.To)}
			if spec.Action == ActionReplace {
				rx.Transform = spec.Transform
			}
			rxMap[target] = append(rxMap[target], rx)
		}
	}
	return rxMap, nil
}

type definePosList []definePos
type definePos struct {
	Pos  token.Pos
	Name []byte
}

func (s definePosList) Len() int      { return len(s) }
func (s definePosList) Swap(i, j int) { s[i], s[j] = s[j], s[i] }
func (s definePosList) Less(i, j int) bool {
	if s[i].Pos != s[j].Pos {
		return s[i].Pos < s[j].Pos
	} else {
		return bytes.Compare(s[i].Name, s[j].Name) < 0
	}
}

func (t *Translator) Printf(format string, args ...interface{}) {
	fmt.Fprintf(t.out, format, args...)
}

func (t *Translator) Learn(macros []int) {
	var defList definePosList
	srcLineByID := make(map[int]string)
	defineByID := make(map[int]int)

	for _, id := range macros {
		name := xc.Dict.S(id)
		if !t.isAcceptableName(TargetConst, name) {
			continue
		}
		pos, tokList, uTokList, ok := cc.ExpandDefine(id)
		if !ok || len(tokList) == 0 {
			continue
		}

		defineByID[id] = tokList[0].Val
		defList = append(defList, definePos{
			Pos:  pos,
			Name: name,
		})

		srcParts := make([]string, len(uTokList))
		for i, v := range uTokList {
			srcParts[i] = cc.TokSrc(v)
		}
		srcLineByID[id] = strings.Join(srcParts, " ")
	}

	sort.Sort(definePosList(defList))

	t.Printf("const (")
	for _, def := range defList {
		id := xc.Dict.ID(def.Name)
		pos := xc.FileSet.Position(def.Pos)
		t.Printf("\n// %s:%d:%d\n//   > define %s %v\n%s = %s",
			narrowPath(pos.Filename), pos.Line, pos.Offset, def.Name, srcLineByID[id],
			t.transformName(TargetConst, string(def.Name)), xc.Dict.S(defineByID[id]))
	}
	t.Printf("\n)\n\n")
}

func (t *Translator) transformName(target RuleTarget, name string) []byte {
	var src []byte
	if target != TargetGlobal {
		// apply global rules
		src = t.transformName(TargetGlobal, name)
	} else {
		src = []byte(name)
	}

	for _, rx := range t.compiledRxs[ActionReplace][target] {
		indices := rx.From.FindAllSubmatchIndex(src, -1)
		reference := make([]byte, 0, len(src))
		reference = append(reference, src...)

		// Itrate submatches backwards since we need to insert expanded
		// versions into the original src and doing so from beginning will shift indices
		// for latter inserts.
		//
		// Example flow:
		// doing title at _partitions in vpx_error_resilient_partitions
		// doing title at _resilient in vpx_error_resilientPartitions
		// doing title at _error in vpx_errorResilientPartitions
		// -> vpxErrorResilientPartitions
		for i := len(indices) - 1; i >= 0; i-- {
			idx := indices[i]
			// if len(rx.Transform) > 0 {
			// 	log.Println("doing", rx.Transform, "at", string(src[idx[0]:idx[1]]), "in", string(src))
			// }
			buf := rx.From.Expand([]byte{}, rx.To, reference, idx)
			switch rx.Transform {
			case TransformLower:
				buf = bytes.ToLower(buf)
			case TransformTitle:
				buf = bytes.Title(buf)
			case TransformUpper:
				buf = bytes.ToUpper(buf)
			}
			src = replaceBytes(src, idx, buf)
		}
	}

	return src
}

func (t *Translator) isAcceptableName(target RuleTarget, name []byte) bool {
	if rxs, ok := t.compiledRxs[ActionIgnore][target]; ok {
		for _, rx := range rxs {
			if rx.From.Match(name) {
				if target != TargetGlobal {
					// fallback to global rules
					return t.isAcceptableName(TargetGlobal, name)
				}
				return false
			}
		}
	}
	if rxs, ok := t.compiledRxs[ActionAccept][target]; ok {
		for _, rx := range rxs {
			if rx.From.Match(name) {
				return true
			}
		}
	}
	if target != TargetGlobal {
		// fallback to global rules
		return t.isAcceptableName(TargetGlobal, name)
	}
	return false
}

func (t *Translator) Translate(unit *cc.TranslationUnit, macros []string) {

}

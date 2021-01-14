// Copyright 2020 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// See the License for the specific language governing permissions and
// limitations under the License.

package placement

import (
	"encoding/json"
	"strings"

	"github.com/pingcap/errors"
)

// Refer to https://github.com/tikv/pd/issues/2701 .
// IMO, it is indeed not bad to have a copy of definition.
// After all, placement rules are communicated using an HTTP API. Loose
//  coupling is a good feature.

// PeerRoleType is the expected peer type of the placement rule.
type PeerRoleType string

const (
	// Voter can either match a leader peer or follower peer.
	Voter PeerRoleType = "voter"
	// Leader matches a leader.
	Leader PeerRoleType = "leader"
	// Follower matches a follower.
	Follower PeerRoleType = "follower"
	// Learner matches a learner.
	Learner PeerRoleType = "learner"
)

// LabelConstraintOp defines how a LabelConstraint matches a store.
type LabelConstraintOp string

const (
	// In restricts the store label value should in the value list.
	// If label does not exist, `in` is always false.
	In LabelConstraintOp = "in"
	// NotIn restricts the store label value should not in the value list.
	// If label does not exist, `notIn` is always true.
	NotIn LabelConstraintOp = "notIn"
	// Exists restricts the store should have the label.
	Exists LabelConstraintOp = "exists"
	// NotExists restricts the store should not have the label.
	NotExists LabelConstraintOp = "notExists"
)

// LabelConstraint is used to filter store when trying to place peer of a region.
type LabelConstraint struct {
	Key    string            `json:"key,omitempty"`
	Op     LabelConstraintOp `json:"op,omitempty"`
	Values []string          `json:"values,omitempty"`
}

// NewLabelConstraint will create a LabelConstraint from string
func NewLabelConstraint(label string) (LabelConstraint, error) {
	r := LabelConstraint{}

	if len(label) < 4 {
		return r, errors.Errorf("label constraint should be in format '{+|-}key=value', but got '%s'", label)
	}

	var op LabelConstraintOp
	switch label[0] {
	case '+':
		op = In
	case '-':
		op = NotIn
	default:
		return r, errors.Errorf("label constraint should be in format '{+|-}key=value', but got '%s'", label)
	}

	kv := strings.Split(label[1:], "=")
	if len(kv) != 2 {
		return r, errors.Errorf("label constraint should be in format '{+|-}key=value', but got '%s'", label)
	}

	key := strings.TrimSpace(kv[0])
	if key == "" {
		return r, errors.Errorf("label constraint should be in format '{+|-}key=value', but got '%s'", label)
	}

	val := strings.TrimSpace(kv[1])
	if val == "" {
		return r, errors.Errorf("label constraint should be in format '{+|-}key=value', but got '%s'", label)
	}

	if op == In && key == EngineLabelKey && strings.ToLower(val) == EngineLabelTiFlash {
		return r, errors.Errorf("unsupported label constraint '%s'", label)
	}

	r.Key = key
	r.Op = op
	r.Values = []string{val}
	return r, nil
}

// Restore converts the LabelConstraint to a string.
func (c *LabelConstraint) Restore() (string, error) {
	var sb strings.Builder
	for i, value := range c.Values {
		switch c.Op {
		case In:
			sb.WriteString("+")
		case NotIn:
			sb.WriteString("-")
		default:
			return "", errors.Errorf("Unsupported label constraint operation: %s", c.Op)
		}
		sb.WriteString(c.Key)
		sb.WriteString("=")
		sb.WriteString(value)
		if i < len(c.Values)-1 {
			sb.WriteString(",")
		}
	}
	return sb.String(), nil
}

// LabelConstraints is a slice of constraints
type LabelConstraints []LabelConstraint

// NewLabelConstraints will check labels, and build LabelConstraints for rule.
func NewLabelConstraints(labels []string) (LabelConstraints, error) {
	constraints := make(LabelConstraints, 0, len(labels))
	for _, str := range labels {
		label, err := NewLabelConstraint(strings.TrimSpace(str))
		if err != nil {
			return constraints, err
		}

		err = constraints.Add(label)
		if err != nil {
			return constraints, err
		}
	}
	return constraints, nil
}

// Restore converts the label constraints to a readable string.
func (constraints *LabelConstraints) Restore() (string, error) {
	var sb strings.Builder
	for i, constraint := range *constraints {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteByte('"')
		conStr, err := constraint.Restore()
		if err != nil {
			return "", err
		}
		sb.WriteString(conStr)
		sb.WriteByte('"')
	}
	return sb.String(), nil
}

// Add will add a new label constraint, with validation
func (constraints *LabelConstraints) Add(label LabelConstraint) error {
	pass := true

	for _, cnst := range *constraints {
		if label.Key == cnst.Key {
			sameOp := label.Op == cnst.Op
			sameVal := label.Values[0] == cnst.Values[0]
			// no following cases:
			// 1. duplicated constraint
			// 2. no instance can meet: +dc=sh, -dc=sh
			// 3. can not match multiple instances: +dc=sh, +dc=bj
			if sameOp && sameVal {
				pass = false
				break
			} else if (!sameOp && sameVal) || (sameOp && !sameVal && label.Op == In) {
				s1, err := label.Restore()
				if err != nil {
					s1 = err.Error()
				}
				s2, err := cnst.Restore()
				if err != nil {
					s2 = err.Error()
				}
				return errors.Errorf("conflicting constraints '%s' and '%s'", s1, s2)
			}
		}
	}

	if pass {
		*constraints = append(*constraints, label)
	}
	return nil
}

// Rule is the placement rule. Check https://github.com/tikv/pd/blob/master/server/schedule/placement/rule.go.
type Rule struct {
	GroupID          string            `json:"group_id"`
	ID               string            `json:"id"`
	Index            int               `json:"index,omitempty"`
	Override         bool              `json:"override,omitempty"`
	StartKeyHex      string            `json:"start_key"`
	EndKeyHex        string            `json:"end_key"`
	Role             PeerRoleType      `json:"role"`
	Count            int               `json:"count"`
	LabelConstraints LabelConstraints  `json:"label_constraints,omitempty"`
	LocationLabels   []string          `json:"location_labels,omitempty"`
	IsolationLevel   string            `json:"isolation_level,omitempty"`
}

// Clone is used to duplicate a RuleOp for safe modification.
func (r *Rule) Clone() *Rule {
	n := &Rule{}
	*n = *r
	return n
}

// Bundle is a group of all rules and configurations. It is used to support rule cache.
type Bundle struct {
	ID       string  `json:"group_id"`
	Index    int     `json:"group_index"`
	Override bool    `json:"group_override"`
	Rules    []*Rule `json:"rules"`
}

func (b *Bundle) String() string {
	t, err := json.Marshal(b)
	if err != nil {
		return ""
	}
	return string(t)
}

// Clone is used to duplicate a bundle.
func (b *Bundle) Clone() *Bundle {
	newBundle := &Bundle{}
	*newBundle = *b
	if len(b.Rules) > 0 {
		newBundle.Rules = make([]*Rule, 0, len(b.Rules))
		for i := range b.Rules {
			newBundle.Rules = append(newBundle.Rules, b.Rules[i].Clone())
		}
	}
	return newBundle
}

// IsEmpty is used to check if a bundle is empty.
func (b *Bundle) IsEmpty() bool {
	return len(b.Rules) == 0 && b.Index == 0 && !b.Override
}

// RuleOpType indicates the operation type.
type RuleOpType string

const (
	// RuleOpAdd a placement rule, only need to specify the field *Rule.
	RuleOpAdd RuleOpType = "add"
	// RuleOpDel a placement rule, only need to specify the field `GroupID`, `ID`, `MatchID`.
	RuleOpDel RuleOpType = "del"
)

// RuleOp is for batching placement rule actions.
type RuleOp struct {
	*Rule
	Action           RuleOpType `json:"action"`
	DeleteByIDPrefix bool       `json:"delete_by_id_prefix"`
}

// Clone is used to clone a RuleOp that is safe to modify, without affecting the old RuleOp.
func (op *RuleOp) Clone() *RuleOp {
	newOp := &RuleOp{}
	*newOp = *op
	newOp.Rule = &Rule{}
	*newOp.Rule = *op.Rule
	return newOp
}

func (op *RuleOp) String() string {
	b, err := json.Marshal(op)
	if err != nil {
		return ""
	}
	return string(b)
}

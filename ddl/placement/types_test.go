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
	"testing"

	. "github.com/pingcap/check"
)

func TestT(t *testing.T) {
	TestingT(t)
}

var _ = Suite(&testLabelConstraintsSuite{})
var _ = Suite(&testLabelConstraintSuite{})
var _ = Suite(&testBundleSuite{})
var _ = Suite(&testRuleSuite{})

type testBundleSuite struct{}

func (t *testBundleSuite) TestEmpty(c *C) {
	bundle := &Bundle{ID: GroupID(1)}
	c.Assert(bundle.IsEmpty(), IsTrue)

	bundle = &Bundle{ID: GroupID(1), Index: 1}
	c.Assert(bundle.IsEmpty(), IsFalse)

	bundle = &Bundle{ID: GroupID(1), Override: true}
	c.Assert(bundle.IsEmpty(), IsFalse)

	bundle = &Bundle{ID: GroupID(1), Rules: []*Rule{{ID: "434"}}}
	c.Assert(bundle.IsEmpty(), IsFalse)

	bundle = &Bundle{ID: GroupID(1), Index: 1, Override: true}
	c.Assert(bundle.IsEmpty(), IsFalse)
}

func (t *testBundleSuite) TestClone(c *C) {
	bundle := &Bundle{ID: GroupID(1), Rules: []*Rule{{ID: "434"}}}

	newBundle := bundle.Clone()
	newBundle.ID = GroupID(2)
	newBundle.Rules[0] = &Rule{ID: "121"}

	c.Assert(bundle, DeepEquals, &Bundle{ID: GroupID(1), Rules: []*Rule{{ID: "434"}}})
	c.Assert(newBundle, DeepEquals, &Bundle{ID: GroupID(2), Rules: []*Rule{{ID: "121"}}})
}

type testRuleSuite struct{}

func (t *testRuleSuite) TestClone(c *C) {
	rule := &Rule{ID: "434"}
	newRule := rule.Clone()
	newRule.ID = "121"

	c.Assert(rule, DeepEquals, &Rule{ID: "434"})
	c.Assert(newRule, DeepEquals, &Rule{ID: "121"})
}

type testLabelConstraintSuite struct{}

func (t *testLabelConstraintSuite) TestNew(c *C) {
	type TestCase struct {
		input string
		label   LabelConstraint
		err    string
	}
	tests := []TestCase{
		{
			input: "+zone=bj",
			label: LabelConstraint{
				Key: "zone",
				Op: In,
				Values: []string{"bj"},
			},
			err: "",
		},
		{
			input: "-  dc  =  sh  ",
			label: LabelConstraint{
				Key: "dc",
				Op: NotIn,
				Values: []string{"sh"},
			},
			err: "",
		},
		{
			input: "-engine  =  tiflash  ",
			label: LabelConstraint{
				Key: "engine",
				Op: NotIn,
				Values: []string{"tiflash"},
			},
			err: "",
		},
		{
			input: "+engine=Tiflash",
			err: ".*unsupported label.*",
		},
		// invald
		{
			input: ",,,",
			err: ".*label constraint should be in format.*",
		},
		{
			input: "+    ",
			err: ".*label constraint should be in format.*",
		},
		{
			input: "0000",
			err: ".*label constraint should be in format.*",
		},
		// without =
		{
			input: "+000",
			err: ".*label constraint should be in format.*",
		},
		// empty key
		{
			input: "+ =zone1",
			err: ".*label constraint should be in format.*",
		},
		{
			input: "+  =   z",
			err: ".*label constraint should be in format.*",
		},
		// empty value
		{
			input: "+zone=",
			err: ".*label constraint should be in format.*",
		},
		{
			input: "+z  =   ",
			err: ".*label constraint should be in format.*",
		},
	}

	for _, t := range testCases {
		if t.err == "" {
			c.Assert(err, IsNil)
		} else {
			c.Assert(err, ErrorMatches, t.err)
		}
	}
}

type testLabelConstraintsSuite struct{}

func (t *testLabelConstraintsSuite) TestNew(c *C) {
	labels, err := NewLabelConstraints([]string{"+zone=sh", "-zone=sh"})
	c.Assert(err, ErrorMatches, ".*conflicting constraints.*")
}

func (t *testLabelConstraintsSuite) TestAdd(c *C) {
	type TestCase struct {
		labels  LabelConstraints
		label   LabelConstraint
		err    string
	}
	tests := []TestCase{}

	labels, err := NewLabelConstraints([]string{"+zone=sh"})
	c.Assert(err, IsNil)
	label, err := checkLabelConstraint("-zone=sh")
	c.Assert(err, IsNil)
	tests = append(tests, TestCase{labels, label, "conflicting constraints.*"})

	labels, err = NewLabelConstraints([]string{"+zone=sh"})
	c.Assert(err, IsNil)
	label, err = checkLabelConstraint("+zone=bj")
	c.Assert(err, IsNil)
	tests = append(tests, TestCase{labels, label, "conflicting constraints.*"})

	labels, err = NewLabelConstraints([]string{"+zone=sh"})
	c.Assert(err, IsNil)
	label, err = checkLabelConstraint("+zone=sh")
	c.Assert(err, IsNil)
	tests = append(tests, TestCase{labels, label, ""})

	for _, t := range tests {
		err := t.labels.Add(t.label)
		if t.err == "" {
			c.Assert(err, IsNil)
		} else {
			c.Assert(err, ErrorMatches, t.err)
		}
	}
}

func (t *testLabelConstraintsSuite) TestRestore(c *C) {
	testCases := []struct {
		constraints    LabelConstraints
		expectedResult string
		expectErr      bool
	}{
		{
			constraints:    LabelConstraints{},
			expectedResult: ``,
		},
		{
			constraints: LabelConstraints{
				{
					Key:    "zone",
					Op:     "in",
					Values: []string{"bj"},
				},
			},
			expectedResult: `"+zone=bj"`,
		},
		{
			constraints: LabelConstraints{
				{
					Key:    "zone",
					Op:     "notIn",
					Values: []string{"bj"},
				},
			},
			expectedResult: `"-zone=bj"`,
		},
		{
			constraints: LabelConstraints{
				{
					Key:    "zone",
					Op:     "exists",
					Values: []string{"bj"},
				},
			},
			expectErr: true,
		},
		{
			constraints: LabelConstraints{
				{
					Key:    "zone",
					Op:     "in",
					Values: []string{"bj", "sh"},
				},
			},
			expectedResult: `"+zone=bj,+zone=sh"`,
		},
		{
			constraints: LabelConstraints{
				{
					Key:    "zone",
					Op:     "in",
					Values: []string{"bj", "sh"},
				},
				{
					Key:    "disk",
					Op:     "in",
					Values: []string{"ssd"},
				},
			},
			expectedResult: `"+zone=bj,+zone=sh","+disk=ssd"`,
		},
	}
	for _, testCase := range testCases {
		rs, err := testCase.constraints.Restore()
		if testCase.expectErr {
			c.Assert(err, NotNil)
		} else {
			c.Assert(rs, Equals, testCase.expectedResult)
		}
	}
}


// Copyright 2021 PingCAP, Inc.
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
	"github.com/xhebox/scoperr"
)

var (
	// InvalidConstraintFormat is from constraint.go.
	InvalidConstraintFormat = errors.New("label constraint should be in format '{+|-}key=value'")
	// UnsupportedConstraint is from constraint.go.
	UnsupportedConstraint   = errors.New("unsupported label constraint")
	// ConflictingConstraints is from constraints.go. 
	ConflictingConstraints  = errors.New("conflicting label constraints")
	// InvalidConstraintsMapcnt is from rule.go. 
	InvalidConstraintsMapcnt = errors.New("label constraints in map syntax have invalid replicas")
	// InvalidConstraintsFormat is from rule.go. 
	InvalidConstraintsFormat = errors.New("invalid label constraints format")
	// InvalidConstraintsRelicas is from rule.go. 
	InvalidConstraintsReplicas = errors.New("label constraints with invalid REPLICAS")
	// InvalidBundleID is from bundle.go. 
	InvalidBundleID = errors.New("invalid bundle ID")
	// InvalidBundleIDFormat is from bundle.go. 
	InvalidBundleIDFormat = errors.New("invalid bundle ID format")
	// LeaderReplicasMustOne is from bundle.go.
	LeaderReplicasMustOne = errors.New("REPLICAS must be 1 if ROLE=leader")
	// MissingRoleField is from bundle.go.
	MissingRoleField = errors.New("the ROLE field is not specified")
	// NoRulesToDrop is from bundle.go.
	NoRulesToDrop = errors.New("no rule of such role to drop")
)

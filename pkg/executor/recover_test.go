// Copyright 2022 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package executor_test

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/pingcap/failpoint"
	ddlutil "github.com/pingcap/tidb/pkg/ddl/util"
	"github.com/pingcap/tidb/pkg/errno"
	"github.com/pingcap/tidb/pkg/infoschema"
	"github.com/pingcap/tidb/pkg/kv"
	"github.com/pingcap/tidb/pkg/meta/model"
	"github.com/pingcap/tidb/pkg/parser/auth"
	"github.com/pingcap/tidb/pkg/sessionctx/variable"
	"github.com/pingcap/tidb/pkg/store/mockstore"
	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/pingcap/tidb/pkg/testkit/testfailpoint"
	"github.com/pingcap/tidb/pkg/types"
	"github.com/pingcap/tidb/pkg/util"
	"github.com/pingcap/tidb/pkg/util/dbterror"
	"github.com/pingcap/tidb/pkg/util/gcutil"
	"github.com/stretchr/testify/require"
	"github.com/tikv/client-go/v2/oracle"
	tikvutil "github.com/tikv/client-go/v2/util"
)

func TestRecoverTable(t *testing.T) {
	require.NoError(t, failpoint.Enable("github.com/pingcap/tidb/pkg/meta/autoid/mockAutoIDChange", `return(true)`))
	defer func() {
		require.NoError(t, failpoint.Disable("github.com/pingcap/tidb/pkg/meta/autoid/mockAutoIDChange"))
	}()

	store := testkit.CreateMockStore(t)
	tk := testkit.NewTestKit(t, store)
	tk.MustExec("create database if not exists test_recover")
	tk.MustExec("use test_recover")
	tk.MustExec("drop table if exists t_recover")
	tk.MustExec("create table t_recover (a int);")

	timeBeforeDrop, timeAfterDrop, safePointSQL, resetGC := MockGC(tk)
	defer resetGC()

	tk.MustExec("insert into t_recover values (1),(2),(3)")
	tk.MustExec("drop table t_recover")

	// if GC safe point is not exists in mysql.tidb
	tk.MustGetErrMsg("recover table t_recover", "can not get 'tikv_gc_safe_point'")
	// set GC safe point
	tk.MustExec(fmt.Sprintf(safePointSQL, timeBeforeDrop))

	// Should recover, and we can drop it straight away.
	tk.MustExec("recover table t_recover")
	tk.MustExec("drop table t_recover")

	require.NoError(t, gcutil.EnableGC(tk.Session()))

	// recover job is before GC safe point
	tk.MustExec(fmt.Sprintf(safePointSQL, timeAfterDrop))
	tk.MustContainErrMsg("recover table t_recover", "Can't find dropped/truncated table 't_recover' in GC safe point")

	// set GC safe point
	tk.MustExec(fmt.Sprintf(safePointSQL, timeBeforeDrop))
	// if there is a new table with the same name, should return failed.
	tk.MustExec("create table t_recover (a int);")
	tk.MustGetErrMsg("recover table t_recover", infoschema.ErrTableExists.GenWithStackByArgs("t_recover").Error())

	// drop the new table with the same name, then recover table.
	tk.MustExec("rename table t_recover to t_recover2")

	// do recover table.
	tk.MustExec("recover table t_recover")

	// check recover table meta and data record.
	tk.MustQuery("select * from t_recover;").Check(testkit.Rows("1", "2", "3"))
	// check recover table autoID.
	tk.MustExec("insert into t_recover values (4),(5),(6)")
	tk.MustQuery("select * from t_recover;").Check(testkit.Rows("1", "2", "3", "4", "5", "6"))
	// check rebase auto id.
	tk.MustQuery("select a,_tidb_rowid from t_recover;").Check(testkit.Rows("1 1", "2 2", "3 3", "4 5001", "5 5002", "6 5003"))

	// recover table by none exits job.
	err := tk.ExecToErr(fmt.Sprintf("recover table by job %d", 10000000))
	require.Error(t, err)

	// recover table by zero JobID.
	// related issue: https://github.com/pingcap/tidb/issues/46296
	err = tk.ExecToErr(fmt.Sprintf("recover table by job %d", 0))
	require.Error(t, err)

	// Disable GC by manual first, then after recover table, the GC enable status should also be disabled.
	require.NoError(t, gcutil.DisableGC(tk.Session()))

	tk.MustExec("delete from t_recover where a > 1")
	tk.MustExec("drop table t_recover")

	tk.MustExec("recover table t_recover")

	// check recover table meta and data record.
	tk.MustQuery("select * from t_recover;").Check(testkit.Rows("1"))
	// check recover table autoID.
	tk.MustExec("insert into t_recover values (7),(8),(9)")
	tk.MustQuery("select * from t_recover;").Check(testkit.Rows("1", "7", "8", "9"))

	// Recover truncate table.
	tk.MustExec("truncate table t_recover")
	tk.MustExec("rename table t_recover to t_recover_new")
	tk.MustExec("recover table t_recover")
	tk.MustExec("insert into t_recover values (10)")
	tk.MustQuery("select * from t_recover;").Check(testkit.Rows("1", "7", "8", "9", "10"))

	// Test for recover one table multiple time.
	tk.MustExec("drop table t_recover")
	tk.MustExec("flashback table t_recover to t_recover_tmp")
	err = tk.ExecToErr("recover table t_recover")
	require.True(t, infoschema.ErrTableExists.Equal(err))

	// Test drop table failed and then recover the table should also be failed.
	tk.MustExec("drop table if exists t_recover2")
	tk.MustExec("create table t_recover2 (a int);")
	jobID := int64(0)
	testfailpoint.EnableCall(t, "github.com/pingcap/tidb/pkg/ddl/beforeRunOneJobStep", func(job *model.Job) {
		if job.Type == model.ActionDropTable && jobID == 0 {
			jobID = job.ID
		}
	})
	tk.MustExec("drop table t_recover2")
	tk.MustExec("recover table by job " + strconv.Itoa(int(jobID)))
	err = tk.ExecToErr("recover table by job " + strconv.Itoa(int(jobID)))
	require.Error(t, err)
	require.Equal(t, "[schema:1050]Table 't_recover2' already been recover to 't_recover2', can't be recover repeatedly", err.Error())

	gcEnable, err := gcutil.CheckGCEnable(tk.Session())
	require.NoError(t, err)
	require.False(t, gcEnable)
}

func TestFlashbackTable(t *testing.T) {
	require.NoError(t, failpoint.Enable("github.com/pingcap/tidb/pkg/meta/autoid/mockAutoIDChange", `return(true)`))
	defer func() {
		require.NoError(t, failpoint.Disable("github.com/pingcap/tidb/pkg/meta/autoid/mockAutoIDChange"))
	}()

	store := testkit.CreateMockStore(t, mockstore.WithStoreType(mockstore.EmbedUnistore))

	tk := testkit.NewTestKit(t, store)
	tk.MustExec("create database if not exists test_flashback")
	tk.MustExec("use test_flashback")
	tk.MustExec("drop table if exists t_flashback")
	tk.MustExec("create table t_flashback (a int);")

	timeBeforeDrop, _, safePointSQL, resetGC := MockGC(tk)
	defer resetGC()

	// Set GC safe point
	tk.MustExec(fmt.Sprintf(safePointSQL, timeBeforeDrop))
	// Set GC enable.
	require.NoError(t, gcutil.EnableGC(tk.Session()))

	tk.MustExec("insert into t_flashback values (1),(2),(3)")
	tk.MustExec("drop table t_flashback")

	// Test flash table with not_exist_table_name name.
	tk.MustGetErrMsg("flashback table t_not_exists", "Can't find localTemporary/dropped/truncated table: t_not_exists in DDL history jobs")

	// Test flashback table failed by there is already a new table with the same name.
	// If there is a new table with the same name, should return failed.
	tk.MustExec("create table t_flashback (a int);")
	tk.MustGetErrMsg("flashback table t_flashback", infoschema.ErrTableExists.GenWithStackByArgs("t_flashback").Error())

	// Drop the new table with the same name, then flashback table.
	tk.MustExec("rename table t_flashback to t_flashback_tmp")

	// Test for flashback table.
	tk.MustExec("flashback table t_flashback")
	// Check flashback table meta and data record.
	tk.MustQuery("select * from t_flashback;").Check(testkit.Rows("1", "2", "3"))
	// Check flashback table autoID.
	tk.MustExec("insert into t_flashback values (4),(5),(6)")
	tk.MustQuery("select * from t_flashback;").Check(testkit.Rows("1", "2", "3", "4", "5", "6"))
	// Check rebase auto id.
	tk.MustQuery("select a,_tidb_rowid from t_flashback;").Check(testkit.Rows("1 1", "2 2", "3 3", "4 5001", "5 5002", "6 5003"))

	// Test for flashback to new table.
	tk.MustExec("drop table t_flashback")
	tk.MustExec("create table t_flashback (a int);")
	tk.MustGetErrMsg("flashback table t_flashback to ` `", dbterror.ErrWrongTableName.GenWithStack("Incorrect table name ' '").Error())
	tk.MustExec("flashback table t_flashback to t_flashback2")
	// Check flashback table meta and data record.
	tk.MustQuery("select * from t_flashback2;").Check(testkit.Rows("1", "2", "3", "4", "5", "6"))
	// Check flashback table autoID.
	tk.MustExec("insert into t_flashback2 values (7),(8),(9)")
	tk.MustQuery("select * from t_flashback2;").Check(testkit.Rows("1", "2", "3", "4", "5", "6", "7", "8", "9"))
	// Check rebase auto id.
	tk.MustQuery("select a,_tidb_rowid from t_flashback2;").Check(testkit.Rows("1 1", "2 2", "3 3", "4 5001", "5 5002", "6 5003", "7 10001", "8 10002", "9 10003"))

	// Test for flashback one table multiple time.
	err := tk.ExecToErr("flashback table t_flashback to t_flashback4")
	require.True(t, infoschema.ErrTableExists.Equal(err))

	// Test for flashback truncated table to new table.
	tk.MustExec("truncate table t_flashback2")
	tk.MustExec("flashback table t_flashback2 to t_flashback3")
	// Check flashback table meta and data record.
	tk.MustQuery("select * from t_flashback3;").Check(testkit.Rows("1", "2", "3", "4", "5", "6", "7", "8", "9"))
	// Check flashback table autoID.
	tk.MustExec("insert into t_flashback3 values (10),(11)")
	tk.MustQuery("select * from t_flashback3;").Check(testkit.Rows("1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11"))
	// Check rebase auto id.
	tk.MustQuery("select a,_tidb_rowid from t_flashback3;").Check(testkit.Rows("1 1", "2 2", "3 3", "4 5001", "5 5002", "6 5003", "7 10001", "8 10002", "9 10003", "10 15001", "11 15002"))

	// Test for flashback drop partition table.
	tk.MustExec("drop table if exists t_p_flashback")
	tk.MustExec("create table t_p_flashback (a int) partition by hash(a) partitions 4;")
	tk.MustExec("insert into t_p_flashback values (1),(2),(3)")
	tk.MustExec("drop table t_p_flashback")
	tk.MustExec("flashback table t_p_flashback")
	// Check flashback table meta and data record.
	tk.MustQuery("select * from t_p_flashback order by a;").Check(testkit.Rows("1", "2", "3"))
	// Check flashback table autoID.
	tk.MustExec("insert into t_p_flashback values (4),(5)")
	tk.MustQuery("select a,_tidb_rowid from t_p_flashback order by a;").Check(testkit.Rows("1 1", "2 2", "3 3", "4 5001", "5 5002"))

	// Test for flashback truncate partition table.
	tk.MustExec("truncate table t_p_flashback")
	tk.MustExec("flashback table t_p_flashback to t_p_flashback1")
	// Check flashback table meta and data record.
	tk.MustQuery("select * from t_p_flashback1 order by a;").Check(testkit.Rows("1", "2", "3", "4", "5"))
	// Check flashback table autoID.
	tk.MustExec("insert into t_p_flashback1 values (6)")
	tk.MustQuery("select a,_tidb_rowid from t_p_flashback1 order by a;").Check(testkit.Rows("1 1", "2 2", "3 3", "4 5001", "5 5002", "6 10001"))

	tk.MustExec("drop database if exists Test2")
	tk.MustExec("create database Test2")
	tk.MustExec("use Test2")
	tk.MustExec("create table t (a int);")
	tk.MustExec("insert into t values (1),(2)")
	tk.MustExec("drop table t")
	tk.MustExec("flashback table t")
	tk.MustQuery("select a from t order by a").Check(testkit.Rows("1", "2"))

	tk.MustExec("drop table t")
	tk.MustExec("drop database if exists Test3")
	tk.MustExec("create database Test3")
	tk.MustExec("use Test3")
	tk.MustExec("create table t (a int);")
	tk.MustExec("drop table t")
	tk.MustExec("drop database Test3")
	tk.MustExec("use Test2")
	tk.MustExec("flashback table t")
	tk.MustExec("insert into t values (3)")
	tk.MustQuery("select a from t order by a").Check(testkit.Rows("1", "2", "3"))
}

func TestRecoverTempTable(t *testing.T) {
	store := testkit.CreateMockStore(t)

	tk := testkit.NewTestKit(t, store)
	tk.MustExec("create database if not exists test_recover")
	tk.MustExec("use test_recover")
	tk.MustExec("drop table if exists t_recover")
	tk.MustExec("create global temporary table t_recover (a int) on commit delete rows;")

	tk.MustExec("use test_recover")
	tk.MustExec("drop table if exists tmp2_recover")
	tk.MustExec("create temporary table tmp2_recover (a int);")

	timeBeforeDrop, _, safePointSQL, resetGC := MockGC(tk)
	defer resetGC()
	// Set GC safe point
	tk.MustExec(fmt.Sprintf(safePointSQL, timeBeforeDrop))

	tk.MustExec("drop table t_recover")
	tk.MustGetErrCode("recover table t_recover;", errno.ErrUnsupportedDDLOperation)
	tk.MustGetErrCode("flashback table t_recover;", errno.ErrUnsupportedDDLOperation)
	tk.MustExec("drop table tmp2_recover")
	tk.MustGetErrMsg("recover table tmp2_recover;", "Can't find localTemporary/dropped/truncated table: tmp2_recover in DDL history jobs")
	tk.MustGetErrMsg("flashback table tmp2_recover;", "Can't find localTemporary/dropped/truncated table: tmp2_recover in DDL history jobs")
}

func TestRecoverTableMeetError(t *testing.T) {
	store := testkit.CreateMockStore(t)
	tk := testkit.NewTestKit(t, store)
	tk.MustExec("set @@GLOBAL.tidb_ddl_error_count_limit=3")
	tk.MustExec("create database if not exists test_recover")
	tk.MustExec("use test_recover")
	tk.MustExec("drop table if exists t_recover")
	tk.MustExec("create table t_recover (a int);")

	timeBeforeDrop, _, safePointSQL, resetGC := MockGC(tk)
	defer resetGC()

	tk.MustExec("insert into t_recover values (1),(2),(3)")
	tk.MustExec("drop table t_recover")

	// Set GC safe point
	tk.MustExec(fmt.Sprintf(safePointSQL, timeBeforeDrop))

	// Should recover, and we can drop it straight away.
	tk.MustExec("recover table t_recover")
	tk.MustQuery("select * from t_recover").Check(testkit.Rows("1", "2", "3"))
	tk.MustExec("drop table t_recover")

	require.NoError(t, failpoint.Enable("github.com/pingcap/tidb/pkg/ddl/mockUpdateVersionAndTableInfoErr", `return(1)`))
	tk.MustContainErrMsg("recover table t_recover", "mock update version and tableInfo error")
	require.NoError(t, failpoint.Disable("github.com/pingcap/tidb/pkg/ddl/mockUpdateVersionAndTableInfoErr"))
	tk.MustContainErrMsg("select * from t_recover", "Table 'test_recover.t_recover' doesn't exist")
}

func TestRecoverTablePrivilege(t *testing.T) {
	store := testkit.CreateMockStore(t)
	tk := testkit.NewTestKit(t, store)

	timeBeforeDrop, _, safePointSQL, resetGC := MockGC(tk)
	defer resetGC()

	// Set GC safe point
	tk.MustExec(fmt.Sprintf(safePointSQL, timeBeforeDrop))

	tk.MustExec("use test")
	tk.MustExec("drop table if exists t_recover")
	tk.MustExec("create table t_recover (a int);")
	tk.MustExec("drop table t_recover")

	// Recover without drop/create privilege.
	tk.MustExec("CREATE USER 'testrecovertable'@'localhost';")
	newTk := testkit.NewTestKit(t, store)
	require.NoError(t, newTk.Session().Auth(&auth.UserIdentity{Username: "testrecovertable", Hostname: "localhost"}, nil, nil, nil))
	newTk.MustGetErrCode("recover table t_recover", errno.ErrTableaccessDenied)
	newTk.MustGetErrCode("flashback table t_recover", errno.ErrTableaccessDenied)

	// Got drop privilege, still failed.
	tk.MustExec("grant drop on *.* to 'testrecovertable'@'localhost';")
	newTk.MustGetErrCode("recover table t_recover", errno.ErrTableaccessDenied)
	newTk.MustGetErrCode("flashback table t_recover", errno.ErrTableaccessDenied)

	// Got select, create and drop privilege, execute success.
	tk.MustExec("grant select,create on *.* to 'testrecovertable'@'localhost';")
	newTk.MustExec("use test")
	newTk.MustExec("recover table t_recover")
	newTk.MustExec("drop table t_recover")
	newTk.MustExec("flashback table t_recover")

	tk.MustExec("drop user 'testrecovertable'@'localhost';")
}

func TestRecoverClusterMeetError(t *testing.T) {
	store := testkit.CreateMockStore(t)
	tk := testkit.NewTestKit(t, store)

	tk.MustContainErrMsg(fmt.Sprintf("flashback cluster to timestamp '%s'", time.Now().Add(30*time.Second)), "Not support flashback cluster in non-TiKV env")

	ts, _ := tk.Session().GetStore().GetOracle().GetTimestamp(context.Background(), &oracle.Option{})
	flashbackTs := oracle.GetTimeFromTS(ts)

	injectSafeTS := oracle.GoTimeToTS(flashbackTs.Add(10 * time.Second))
	require.NoError(t, failpoint.Enable("github.com/pingcap/tidb/pkg/ddl/mockFlashbackTest", `return(true)`))
	require.NoError(t, failpoint.Enable("github.com/pingcap/tidb/pkg/ddl/injectSafeTS",
		fmt.Sprintf("return(%v)", injectSafeTS)))

	// Get GC safe point error.
	tk.MustContainErrMsg(fmt.Sprintf("flashback cluster to timestamp '%s'", time.Now().Add(30*time.Second)), "cannot set flashback timestamp to future time")
	tk.MustContainErrMsg(fmt.Sprintf("flashback cluster to timestamp '%s'", time.Now().Add(0-30*time.Second)), "can not get 'tikv_gc_safe_point'")

	timeBeforeDrop, _, safePointSQL, resetGC := MockGC(tk)
	defer resetGC()

	// Set GC safe point.
	tk.MustExec(fmt.Sprintf(safePointSQL, timeBeforeDrop))

	// out of GC safe point range.
	tk.MustGetErrCode(fmt.Sprintf("flashback cluster to timestamp '%s'", time.Now().Add(0-60*60*60*time.Second)), int(variable.ErrSnapshotTooOld.Code()))

	// Flashback without super privilege.
	tk.MustExec("CREATE USER 'testflashback'@'localhost';")
	newTk := testkit.NewTestKit(t, store)
	require.NoError(t, newTk.Session().Auth(&auth.UserIdentity{Username: "testflashback", Hostname: "localhost"}, nil, nil, nil))
	newTk.MustGetErrCode(fmt.Sprintf("flashback cluster to timestamp '%s'", time.Now().Add(0-30*time.Second)), errno.ErrPrivilegeCheckFail)
	tk.MustExec("drop user 'testflashback'@'localhost';")

	// detect modify system table
	nowTS, err := tk.Session().GetStore().GetOracle().GetTimestamp(context.Background(), &oracle.Option{})
	require.NoError(t, err)
	tk.MustExec("truncate table mysql.stats_meta")
	errorMsg := fmt.Sprintf("[ddl:-1]Detected modified system table during [%s, now), can't do flashback", oracle.GetTimeFromTS(nowTS).Format(types.TimeFSPFormat))
	tk.MustGetErrMsg(fmt.Sprintf("flashback cluster to timestamp '%s'", oracle.GetTimeFromTS(nowTS).Format(types.TimeFSPFormat)), errorMsg)

	// update tidb_server_version
	nowTS, err = tk.Session().GetStore().GetOracle().GetTimestamp(context.Background(), &oracle.Option{})
	require.NoError(t, err)
	tk.MustExec("update mysql.tidb set VARIABLE_VALUE=VARIABLE_VALUE+1 where VARIABLE_NAME='tidb_server_version'")
	errorMsg = fmt.Sprintf("[ddl:-1]Detected TiDB upgrade during [%s, now), can't do flashback", oracle.GetTimeFromTS(nowTS).Format(types.TimeFSPFormat))
	tk.MustGetErrMsg(fmt.Sprintf("flashback cluster to timestamp '%s'", oracle.GetTimeFromTS(nowTS).Format(types.TimeFSPFormat)), errorMsg)

	require.NoError(t, failpoint.Disable("github.com/pingcap/tidb/pkg/ddl/injectSafeTS"))
	require.NoError(t, failpoint.Disable("github.com/pingcap/tidb/pkg/ddl/mockFlashbackTest"))
}

func TestFlashbackWithSafeTs(t *testing.T) {
	store := testkit.CreateMockStore(t)
	tk := testkit.NewTestKit(t, store)

	require.NoError(t, failpoint.Enable("github.com/pingcap/tidb/pkg/ddl/mockFlashbackTest", `return(true)`))
	require.NoError(t, failpoint.Enable("github.com/pingcap/tidb/pkg/ddl/changeFlashbackGetMinSafeTimeTimeout", `return(0)`))

	timeBeforeDrop, _, safePointSQL, resetGC := MockGC(tk)
	defer resetGC()

	// Set GC safe point.
	tk.MustExec(fmt.Sprintf(safePointSQL, timeBeforeDrop))

	time.Sleep(time.Second)
	ts, _ := tk.Session().GetStore().GetOracle().GetTimestamp(context.Background(), &oracle.Option{})
	flashbackTs := oracle.GetTimeFromTS(ts)
	testcases := []struct {
		name         string
		sql          string
		injectSafeTS uint64
		// compareWithSafeTS will be 0 if FlashbackTS==SafeTS, -1 if FlashbackTS < SafeTS, and +1 if FlashbackTS > SafeTS.
		compareWithSafeTS int
	}{
		{
			name:              "5 seconds ago to now, safeTS 5 secs ago",
			sql:               fmt.Sprintf("flashback cluster to timestamp '%s'", flashbackTs),
			injectSafeTS:      oracle.GoTimeToTS(flashbackTs),
			compareWithSafeTS: 0,
		},
		{
			name:              "10 seconds ago to now, safeTS 5 secs ago",
			sql:               fmt.Sprintf("flashback cluster to timestamp '%s'", flashbackTs),
			injectSafeTS:      oracle.GoTimeToTS(flashbackTs.Add(10 * time.Second)),
			compareWithSafeTS: -1,
		},
		{
			name:              "5 seconds ago to now, safeTS 10 secs ago",
			sql:               fmt.Sprintf("flashback cluster to timestamp '%s'", flashbackTs),
			injectSafeTS:      oracle.GoTimeToTS(flashbackTs.Add(-10 * time.Second)),
			compareWithSafeTS: 1,
		},
	}
	for _, testcase := range testcases {
		t.Log(testcase.name)
		require.NoError(t, failpoint.Enable("github.com/pingcap/tidb/pkg/ddl/injectSafeTS",
			fmt.Sprintf("return(%v)", testcase.injectSafeTS)))
		if testcase.compareWithSafeTS == 1 {
			start := time.Now()
			tk.MustContainErrMsg(testcase.sql,
				"cannot set flashback timestamp after min-resolved-ts")
			// When set `flashbackGetMinSafeTimeTimeout` = 0, no retry for `getStoreGlobalMinSafeTS`.
			require.Less(t, time.Since(start), time.Second)
		} else {
			tk.MustExec(testcase.sql)
		}
	}
	require.NoError(t, failpoint.Disable("github.com/pingcap/tidb/pkg/ddl/injectSafeTS"))
	require.NoError(t, failpoint.Disable("github.com/pingcap/tidb/pkg/ddl/mockFlashbackTest"))
	require.NoError(t, failpoint.Disable("github.com/pingcap/tidb/pkg/ddl/changeFlashbackGetMinSafeTimeTimeout"))
}

func TestFlashbackTSOWithSafeTs(t *testing.T) {
	store := testkit.CreateMockStore(t)
	tk := testkit.NewTestKit(t, store)

	require.NoError(t, failpoint.Enable("github.com/pingcap/tidb/pkg/ddl/mockFlashbackTest", `return(true)`))
	require.NoError(t, failpoint.Enable("github.com/pingcap/tidb/pkg/ddl/changeFlashbackGetMinSafeTimeTimeout", `return(0)`))

	timeBeforeDrop, _, safePointSQL, resetGC := MockGC(tk)
	defer resetGC()

	// Set GC safe point.
	tk.MustExec(fmt.Sprintf(safePointSQL, timeBeforeDrop))

	time.Sleep(time.Second)
	ts, _ := tk.Session().GetStore().GetOracle().GetTimestamp(context.Background(), &oracle.Option{})
	flashbackTs := oracle.GetTimeFromTS(ts)
	testcases := []struct {
		name         string
		sql          string
		injectSafeTS uint64
		// compareWithSafeTS will be 0 if FlashbackTS==SafeTS, -1 if FlashbackTS < SafeTS, and +1 if FlashbackTS > SafeTS.
		compareWithSafeTS int
	}{
		{
			name:              "5 seconds ago to now, safeTS 5 secs ago",
			sql:               fmt.Sprintf("flashback cluster to tso %d", ts),
			injectSafeTS:      oracle.GoTimeToTS(flashbackTs),
			compareWithSafeTS: 0,
		},
		{
			name:              "10 seconds ago to now, safeTS 5 secs ago",
			sql:               fmt.Sprintf("flashback cluster to tso %d", ts),
			injectSafeTS:      oracle.GoTimeToTS(flashbackTs.Add(10 * time.Second)),
			compareWithSafeTS: -1,
		},
		{
			name:              "5 seconds ago to now, safeTS 10 secs ago",
			sql:               fmt.Sprintf("flashback cluster to tso %d", ts),
			injectSafeTS:      oracle.GoTimeToTS(flashbackTs.Add(-10 * time.Second)),
			compareWithSafeTS: 1,
		},
	}
	for _, testcase := range testcases {
		t.Log(testcase.name)
		require.NoError(t, failpoint.Enable("github.com/pingcap/tidb/pkg/ddl/injectSafeTS",
			fmt.Sprintf("return(%v)", testcase.injectSafeTS)))
		if testcase.compareWithSafeTS == 1 {
			start := time.Now()
			tk.MustContainErrMsg(testcase.sql,
				"cannot set flashback timestamp after min-resolved-ts")
			// When set `flashbackGetMinSafeTimeTimeout` = 0, no retry for `getStoreGlobalMinSafeTS`.
			require.Less(t, time.Since(start), time.Second)
		} else {
			tk.MustExec(testcase.sql)
		}
	}
	require.NoError(t, failpoint.Disable("github.com/pingcap/tidb/pkg/ddl/injectSafeTS"))
	require.NoError(t, failpoint.Disable("github.com/pingcap/tidb/pkg/ddl/mockFlashbackTest"))
	require.NoError(t, failpoint.Disable("github.com/pingcap/tidb/pkg/ddl/changeFlashbackGetMinSafeTimeTimeout"))
}

func TestFlashbackRetryGetMinSafeTime(t *testing.T) {
	store := testkit.CreateMockStore(t)
	tk := testkit.NewTestKit(t, store)

	require.NoError(t, failpoint.Enable("github.com/pingcap/tidb/pkg/ddl/mockFlashbackTest", `return(true)`))

	timeBeforeDrop, _, safePointSQL, resetGC := MockGC(tk)
	defer resetGC()

	// Set GC safe point.
	tk.MustExec(fmt.Sprintf(safePointSQL, timeBeforeDrop))

	time.Sleep(time.Second)
	ts, _ := tk.Session().GetStore().GetOracle().GetTimestamp(context.Background(), &oracle.Option{})
	flashbackTs := oracle.GetTimeFromTS(ts)

	require.NoError(t, failpoint.Enable("github.com/pingcap/tidb/pkg/ddl/injectSafeTS",
		fmt.Sprintf("return(%v)", oracle.GoTimeToTS(flashbackTs.Add(-10*time.Minute)))))

	go func() {
		time.Sleep(2 * time.Second)
		require.NoError(t, failpoint.Enable("github.com/pingcap/tidb/pkg/ddl/injectSafeTS",
			fmt.Sprintf("return(%v)", oracle.GoTimeToTS(flashbackTs.Add(10*time.Minute)))))
	}()

	start := time.Now()
	tk.MustExec(fmt.Sprintf("flashback cluster to timestamp '%s'", flashbackTs))
	duration := time.Since(start)
	require.Greater(t, duration, 2*time.Second)
	require.Less(t, duration, 5*time.Second)

	require.NoError(t, failpoint.Disable("github.com/pingcap/tidb/pkg/ddl/injectSafeTS"))
	require.NoError(t, failpoint.Disable("github.com/pingcap/tidb/pkg/ddl/mockFlashbackTest"))
}

func TestFlashbackSchema(t *testing.T) {
	testfailpoint.Enable(t, "github.com/pingcap/tidb/pkg/meta/autoid/mockAutoIDChange", `return(true)`)

	store := testkit.CreateMockStore(t, mockstore.WithStoreType(mockstore.EmbedUnistore))

	tk := testkit.NewTestKit(t, store)
	tk.MustExec("set @@global.tidb_ddl_error_count_limit = 2")
	tk.MustExec("create database if not exists test_flashback")
	tk.MustExec("use test_flashback")
	tk.MustExec("drop table if exists t_flashback")
	tk.MustExec("create table t_flashback (a int)")

	timeBeforeDrop, _, safePointSQL, resetGC := MockGC(tk)
	defer resetGC()

	// if GC safe point is not exists in mysql.tidb
	tk.MustGetErrMsg("flashback database db_not_exists", "can not get 'tikv_gc_safe_point'")
	// Set GC safe point
	tk.MustExec(fmt.Sprintf(safePointSQL, timeBeforeDrop))
	// Set GC enable.
	require.NoError(t, gcutil.EnableGC(tk.Session()))

	tk.MustExec("insert into t_flashback values (1),(2),(3)")
	tk.MustExec("drop database test_flashback")

	// even PD is down, the job can not be canceled for now.
	testfailpoint.Enable(t, "github.com/pingcap/tidb/pkg/ddl/mockClearTablePlacementAndBundlesErr", `4*return()`)
	tk.MustExec("flashback database test_flashback")
	testfailpoint.Disable(t, "github.com/pingcap/tidb/pkg/ddl/mockClearTablePlacementAndBundlesErr")

	// Test flashback database with db_not_exists name.
	tk.MustGetErrMsg("flashback database db_not_exists", "Can't find dropped database: db_not_exists in DDL history jobs")
	tk.MustGetErrMsg("flashback database test_flashback to test_flashback2", infoschema.ErrDatabaseExists.GenWithStack("Schema 'test_flashback' already been recover to 'test_flashback', can't be recover repeatedly").Error())

	// Test flashback database failed by there is already a new database with the same name.
	// If there is a new database with the same name, should return failed.
	tk.MustExec("create database db_flashback")
	tk.MustGetErrMsg("flashback schema db_flashback", infoschema.ErrDatabaseExists.GenWithStackByArgs("db_flashback").Error())

	//  Test for flashback schema.
	tk.MustExec("drop database if exists test1")
	tk.MustExec("create database test1")
	tk.MustExec("use test1")
	tk.MustExec("create table t (a int)")
	tk.MustExec("create table t1 (a int)")
	tk.MustExec("insert into t values (1),(2),(3)")
	tk.MustExec("insert into t1 values (4),(5),(6)")
	tk.MustExec("drop database test1")
	tk.MustExec("flashback schema test1")
	tk.MustExec("use test1")
	tk.MustQuery("select a from t order by a").Check(testkit.Rows("1", "2", "3"))
	tk.MustQuery("select a from t1 order by a").Check(testkit.Rows("4", "5", "6"))
	tk.MustExec("drop database test1")
	tk.MustExec("flashback schema test1 to test2")
	tk.MustExec("use test2")
	tk.MustQuery("select a from t order by a").Check(testkit.Rows("1", "2", "3"))
	tk.MustQuery("select a from t1 order by a").Check(testkit.Rows("4", "5", "6"))

	tk.MustExec("drop database if exists t_recover")
	tk.MustExec("create database t_recover")
	tk.MustExec("drop database t_recover")

	// Recover without drop/create privilege.
	tk.MustExec("CREATE USER 'testflashbackschema'@'localhost';")
	newTk := testkit.NewTestKit(t, store)
	require.NoError(t, newTk.Session().Auth(&auth.UserIdentity{Username: "testflashbackschema", Hostname: "localhost"}, nil, nil, nil))
	newTk.MustGetErrCode("flashback database t_recover", errno.ErrDBaccessDenied)

	// Got drop privilege, still failed.
	tk.MustExec("grant drop on *.* to 'testflashbackschema'@'localhost';")
	newTk.MustGetErrCode("flashback database t_recover", errno.ErrDBaccessDenied)

	// Got create and drop privilege, execute success.
	tk.MustExec("grant create on *.* to 'testflashbackschema'@'localhost';")
	newTk.MustExec("flashback schema t_recover")

	tk.MustExec("drop user 'testflashbackschema'@'localhost';")
}

func TestFlashbackSchemaWithManyTables(t *testing.T) {
	testfailpoint.Enable(t, "github.com/pingcap/tidb/pkg/meta/autoid/mockAutoIDChange", `return(true)`)

	backup := kv.TxnEntrySizeLimit.Load()
	kv.TxnEntrySizeLimit.Store(50000)
	t.Cleanup(func() {
		kv.TxnEntrySizeLimit.Store(backup)
	})

	store := testkit.CreateMockStore(t, mockstore.WithStoreType(mockstore.EmbedUnistore))

	tk := testkit.NewTestKit(t, store)
	tk.MustExec("set @@global.tidb_ddl_error_count_limit = 2")
	tk.MustExec("set @@global.tidb_enable_fast_create_table=ON")
	tk.MustExec("drop database if exists many_tables")
	tk.MustExec("create database if not exists many_tables")
	tk.MustExec("use many_tables")

	timeBeforeDrop, _, safePointSQL, resetGC := MockGC(tk)
	defer resetGC()

	// Set GC safe point
	tk.MustExec(fmt.Sprintf(safePointSQL, timeBeforeDrop))
	// Set GC enable.
	require.NoError(t, gcutil.EnableGC(tk.Session()))

	var wg util.WaitGroupWrapper
	for i := range 10 {
		idx := i
		wg.Run(func() {
			tkit := testkit.NewTestKit(t, store)
			tkit.MustExec("use many_tables")
			for j := range 70 {
				tkit.MustExec(fmt.Sprintf("create table t_%d_%d (a int)", idx, j))
			}
		})
	}
	wg.Wait()

	tk.MustExec("drop database many_tables")

	tk.MustExec("flashback database many_tables")

	tk.MustQuery("select count(*) from many_tables.t_0_0").Check(testkit.Rows("0"))
}

// MockGC is used to make GC work in the test environment.
func MockGC(tk *testkit.TestKit) (string, string, string, func()) {
	originGC := ddlutil.IsEmulatorGCEnable()
	resetGC := func() {
		if originGC {
			ddlutil.EmulatorGCEnable()
		} else {
			ddlutil.EmulatorGCDisable()
		}
	}

	// disable emulator GC.
	// Otherwise emulator GC will delete table record as soon as possible after execute drop table ddl.
	ddlutil.EmulatorGCDisable()
	timeBeforeDrop := time.Now().Add(0 - 48*60*60*time.Second).Format(tikvutil.GCTimeFormat)
	timeAfterDrop := time.Now().Add(48 * 60 * 60 * time.Second).Format(tikvutil.GCTimeFormat)
	safePointSQL := `INSERT HIGH_PRIORITY INTO mysql.tidb VALUES ('tikv_gc_safe_point', '%[1]s', '')
			       ON DUPLICATE KEY
			       UPDATE variable_value = '%[1]s'`
	// clear GC variables first.
	tk.MustExec("delete from mysql.tidb where variable_name in ( 'tikv_gc_safe_point','tikv_gc_enable' )")
	return timeBeforeDrop, timeAfterDrop, safePointSQL, resetGC
}

func TestFlashbackClusterWithManyDBs(t *testing.T) {
	store := testkit.CreateMockStore(t)
	tk := testkit.NewTestKit(t, store)

	timeBeforeDrop, _, safePointSQL, resetGC := MockGC(tk)
	defer resetGC()

	// Set GC safe point.
	tk.MustExec(fmt.Sprintf(safePointSQL, timeBeforeDrop))

	backup := kv.TxnEntrySizeLimit.Load()
	kv.TxnEntrySizeLimit.Store(50000)
	t.Cleanup(func() {
		kv.TxnEntrySizeLimit.Store(backup)
	})

	tk.MustExec("set @@global.tidb_ddl_error_count_limit = 2")
	tk.MustExec("set @@global.tidb_enable_fast_create_table=ON")

	var wg sync.WaitGroup
	dbPerWorker := 10
	for i := range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tk2 := testkit.NewTestKit(t, store)
			for j := range dbPerWorker {
				dbName := fmt.Sprintf("db_%d", i*dbPerWorker+j)
				tk2.MustExec(fmt.Sprintf("create database %s", dbName))
			}
		}()
	}

	wg.Wait()

	ts, _ := store.CurrentVersion(oracle.GlobalTxnScope)
	flashbackTs := oracle.GetTimeFromTS(ts.Ver)

	injectSafeTS := oracle.GoTimeToTS(flashbackTs.Add(10 * time.Second))
	testfailpoint.Enable(t, "github.com/pingcap/tidb/pkg/ddl/mockFlashbackTest", `return(true)`)
	testfailpoint.Enable(t, "github.com/pingcap/tidb/pkg/ddl/injectSafeTS",
		fmt.Sprintf("return(%v)", injectSafeTS))

	// this test will fail before the fix, because the DDL job KV entry is too large.
	tk.MustExec(fmt.Sprintf("flashback cluster to timestamp '%s'", flashbackTs))
}

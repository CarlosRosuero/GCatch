package pointer

import (
	"fmt"
	"github.com/system-pclub/GCatch/GCatch/config"
	"github.com/system-pclub/GCatch/GCatch/instinfo"
	"github.com/system-pclub/GCatch/GCatch/output"
	"github.com/system-pclub/GCatch/GCatch/tools/go/callgraph"
	"github.com/system-pclub/GCatch/GCatch/tools/go/mypointer"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
	"strconv"
	"strings"
)

// AnalyzeAllSyncOp first finds all sync operations and corresponding values, which will be returned
// It then runs the pointer analysis for each value, and return the result
func AnalyzeAllSyncOp() (*mypointer.Result, []*instinfo.StOpValue) {
	vecStOpValue := []*instinfo.StOpValue{}
	for fn, _ := range ssautil.AllFunctions(config.Prog) {
		if fn == nil {
			continue
		}
		// Note that we scan every available functions here, because we don't know where a chan will be passed to
		for _, bb := range fn.Blocks {
			for _, inst := range bb.Instrs {
				// case 1: traditional
				v, comment := instinfo.ScanInstFindLockerValue(inst)
				if v != nil {
					newStOpValue := &instinfo.StOpValue{
						Inst:    inst,
						Value:   v,
						Comment: comment,
					}
					vecStOpValue = append(vecStOpValue, newStOpValue)
					continue
				}

				// case 2: channel
				chs, comments := instinfo.ScanInstFindChanValue(inst)
				for i, ch := range chs {
					if ch == nil {
						continue
					}
					newStOpValue := &instinfo.StOpValue{
						Inst:    inst,
						Value:   chs[i],
						Comment: comments[i],
					}
					vecStOpValue = append(vecStOpValue, newStOpValue)
				}
			}
		}
	}

	queries := make(map[ssa.Value]struct{})
	for _, stOpValue := range vecStOpValue {
		queries[stOpValue.Value] = struct{}{}
	}
	cfg := &mypointer.Config{
		OLDMains:        nil,
		Prog:            config.Prog,
		Reflection:      false,
		BuildCallGraph:  true,
		Queries:         queries,
		IndirectQueries: nil,
		Log:             nil,
	}
	stPtrResult, err := mypointer.Analyze(cfg, nil)
	if err != nil {
		fmt.Println("Error when querying all channel values:\n", err.Error())
		return nil, nil
	}

	// Update config.Callgraph, and create a map from instruction to all its corresponding out edges in CallGraph
	config.CallGraph = stPtrResult.CallGraph

	config.Inst2CallSite = make(map[ssa.CallInstruction]map[*callgraph.Edge]bool)
	for _, node := range config.CallGraph.Nodes {
		for _, out := range node.Out {
			mapCallSites, boolExist := config.Inst2CallSite[out.Site]
			if !boolExist {
				mapCallSites = make(map[*callgraph.Edge]bool)
				config.Inst2CallSite[out.Site] = mapCallSites
			}

			mapCallSites[out] = true
		}
	}

	return stPtrResult, vecStOpValue
}

func WithdrawAllChan(stPtrResult *mypointer.Result, vecStOpValue []*instinfo.StOpValue) (result []*instinfo.Channel) {
	vecStChanOpAndValue := []*instinfo.StOpValue{}
	for _, syncInstValue := range vecStOpValue {
		switch syncInstValue.Comment {
		case instinfo.Send, instinfo.Recv, instinfo.MakeChan, instinfo.Close:
			vecStChanOpAndValue = append(vecStChanOpAndValue, syncInstValue)
		default: // Select or Mutex/Cond/Waitgroup
			if strings.Contains(syncInstValue.Comment, "Select_") {
				vecStChanOpAndValue = append(vecStChanOpAndValue, syncInstValue)
			}
		}
	}

	label2ChOp := mergeAlias(vecStChanOpAndValue, stPtrResult)
	for label, ch_ops := range label2ChOp {
		boolInContext := boolIsInContext(label.Value())
		boolInTime := boolIsInTime(label.Value())

		var chPrim *instinfo.Channel
		if boolInContext { // let these ops belong to a special channel
			chPrim = &instinfo.ChanContext
		} else if boolInTime {
			chPrim = &instinfo.ChanTimer
		} else {
			chPrim = &instinfo.Channel{
				Name:     "",
				MakeInst: nil,
				Pkg:      "",
				Buffer:   0,
				Sends:    nil,
				Recvs:    nil,
				Closes:   nil,
				Status:   "",
			}
			result = append(result, chPrim)
		}

		for _, chOp := range ch_ops {
			switch chOp.Comment {
			case instinfo.MakeChan:
				new_make := &instinfo.ChMake{
					ChOp: instinfo.ChOp{
						Parent: chPrim,
						Inst:   chOp.Inst,
					},
				}
				///DELETE
				if chOp.Inst.Parent().Name() == "TestPipeListener" {
					fmt.Print()
				}

				chPrim.Make = new_make
				chPrim.MakeInst = chOp.Inst.(*ssa.MakeChan)
				pkg := chOp.Inst.Parent().Pkg
				if pkg != nil {
					chPrim.Pkg = pkg.String()
				} else {
					chPrim.Pkg = ""
				}
				instMakechan, ok := chOp.Inst.(*ssa.MakeChan)
				if !ok {
					fmt.Println("Error: convert inst to *ssa.MakeChan failed. Inst:")
					output.PrintIISrc(chOp.Inst)
					continue
				}
				// store the buffer size
				bv := instMakechan.Size
				bvConst, ok := bv.(*ssa.Const)
				if !ok { // Dynamic size
					chPrim.Buffer = instinfo.DynamicSize
					continue
				}
				defer func(inst ssa.Instruction) {
					if r := recover(); r != nil { // I am concerned that bvConst.Int64() may panic, though it never happens
						fmt.Println("Recovered when dealing with:", inst)
						output.PrintIISrc(inst)
					}
				}(chOp.Inst)
				intBuffer := bvConst.Int64()
				chPrim.Buffer = int(intBuffer)

			case instinfo.Send:
				newSend := &instinfo.ChSend{
					CaseIndex:      -1,
					IsCaseBlocking: false,
					Status:         "",
					ChOp: instinfo.ChOp{
						Parent: chPrim,
						Inst:   chOp.Inst,
					},
				}
				chPrim.Sends = append(chPrim.Sends, newSend)
			case instinfo.Recv:
				newRecv := &instinfo.ChRecv{
					CaseIndex:      -1,
					IsCaseBlocking: false,
					Status:         "",
					ChOp: instinfo.ChOp{
						Parent: chPrim,
						Inst:   chOp.Inst,
					},
				}
				chPrim.Recvs = append(chPrim.Recvs, newRecv)
			case instinfo.Close:
				_, boolIsDefer := chOp.Inst.(*ssa.Defer)
				newClose := &instinfo.ChClose{
					IsDefer: boolIsDefer,
					Status:  "",
					ChOp: instinfo.ChOp{
						Parent: chPrim,
						Inst:   chOp.Inst,
					},
				}
				chPrim.Closes = append(chPrim.Closes, newClose)
			default:
				//Select
				if i := strings.Index(chOp.Comment, "Select_Send_"); i > -1 {
					var boolIsBlocking bool
					if strings.HasPrefix(chOp.Comment, "Non_Blocking") {
						boolIsBlocking = false
					} else {
						boolIsBlocking = true
					}
					caseIndex, err := strconv.Atoi(chOp.Comment[i+12:])
					if err != nil {
						fmt.Println("Error when conv str to int for select inst:", err)
						output.PrintIISrc(chOp.Inst)
					}
					newSend := &instinfo.ChSend{
						CaseIndex:      caseIndex,
						IsCaseBlocking: boolIsBlocking,
						Status:         "",
						ChOp: instinfo.ChOp{
							Parent: chPrim,
							Inst:   chOp.Inst,
						},
					}
					chPrim.Sends = append(chPrim.Sends, newSend)
				} else if i := strings.Index(chOp.Comment, "Select_Recv_"); i > -1 {
					var boolIsBlocking bool
					if strings.HasPrefix(chOp.Comment, "Non_Blocking") {
						boolIsBlocking = false
					} else {
						boolIsBlocking = true
					}
					caseIndex, err := strconv.Atoi(chOp.Comment[i+12:])
					if err != nil {
						fmt.Println("Error when conv str to int for select inst:", err)
						output.PrintIISrc(chOp.Inst)
					}
					new_recv := &instinfo.ChRecv{
						CaseIndex:      caseIndex,
						IsCaseBlocking: boolIsBlocking,
						Status:         "",
						ChOp: instinfo.ChOp{
							Parent: chPrim,
							Inst:   chOp.Inst,
						},
					}
					chPrim.Recvs = append(chPrim.Recvs, new_recv)
				}
			}
		}

		if !boolInContext && !boolInTime {
			recordChInstToMap(chPrim)
		}
	}

	recordChInstToMap(&instinfo.ChanTimer)
	recordChInstToMap(&instinfo.ChanContext)
	result = append(result, &instinfo.ChanTimer)
	result = append(result, &instinfo.ChanContext)

	return
}

func WithdrawAllTraditionals(stPtrResult *mypointer.Result, vecStOpValue []*instinfo.StOpValue) (result []*instinfo.Locker) {
	vecStTradOpAndValue := []*instinfo.StOpValue{}
	for _, stOpValue := range vecStOpValue {
		switch stOpValue.Comment {
		case instinfo.Lock, instinfo.Unlock:
			vecStTradOpAndValue = append(vecStTradOpAndValue, stOpValue)

		// If we need to handle RWMutex/Waitgroup/Cond, add cases here

		default:

		}
	}

	label2LockerOp := mergeAlias(vecStTradOpAndValue, stPtrResult)
	for label, lockerOps := range label2LockerOp {
		if label.Value() == nil {
			fmt.Println("Warning in WithdrawAllTraditionals: label of locker has nil value:", label.Value())
			fmt.Println("First 3 Ops, if any:")
			count := 0
			for _, op := range lockerOps {
				if count > 2 {
					continue
				}
				count++
				output.PrintIISrc(op.Inst)
			}
			continue
		}
		var strlockerType string
		if strings.Contains(label.Value().Type().String(), "RWMutex") {
			strlockerType = instinfo.RWMutex
		} else {
			strlockerType = instinfo.Mutex
		}
		newLocker := &instinfo.Locker{
			Name:    "",
			Type:    strlockerType,
			Locks:   nil,
			Unlocks: nil,
			Pkg:     "",
			Status:  "",
			Value:   label.Value(),
		}
		strFnLabel := label.Value().Parent()
		if strFnLabel != nil && strFnLabel.Pkg != nil {
			newLocker.Pkg = strFnLabel.Pkg.Pkg.String()
		}
		for _, lockerOp := range lockerOps {
			switch lockerOp.Comment {
			case instinfo.Lock:
				newLock := &instinfo.LockOp{
					Name:    "",
					Inst:    lockerOp.Inst,
					IsRLock: false,
					IsDefer: false,
					Parent:  newLocker,
				}
				if _, ok := lockerOp.Inst.(*ssa.Defer); ok {
					newLock.IsDefer = true
				}
				newLocker.Locks = append(newLocker.Locks, newLock)
				instinfo.MapInst2LockerOp[newLock.Inst] = newLock

			case instinfo.Unlock:
				newUnlock := &instinfo.UnlockOp{
					Name:      "",
					Inst:      lockerOp.Inst,
					IsRUnlock: false,
					IsDefer:   false,
					Parent:    newLocker,
				}
				if _, ok := lockerOp.Inst.(*ssa.Defer); ok {
					newUnlock.IsDefer = true
				}
				newLocker.Unlocks = append(newLocker.Unlocks, newUnlock)
				instinfo.MapInst2LockerOp[newUnlock.Inst] = newUnlock
			default:
			}
		}

		result = append(result, newLocker)
	}

	return
}

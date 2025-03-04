package doublelock

import (
	"fmt"
	"github.com/system-pclub/GCatch/GCatch/util"
	"golang.org/x/tools/go/ssa"
)

var mapGen map[ssa.Instruction]*StLockingOp
var mapKill map[ssa.Instruction]*StUnlockingOp
var mapBefore map[ssa.Instruction]map[*StLockingOp]bool
var mapAfter map[ssa.Instruction]map[*StLockingOp]bool

func printMapBefore() {

	fmt.Println()
	fmt.Println("Print Before Map:")

	for ii, m := range mapBefore {
		if len(m) == 0 {
			continue
		}

		fmt.Print(ii, " (before): ")

		for l, _ := range m {
			fmt.Print(l.StrName)
			fmt.Print(" ")
		}

		fmt.Println()
	}
}

func printMapGen() {
	fmt.Println()
	fmt.Println("Print Gen Map:")

	for ii, l := range mapGen {
		fmt.Print(ii, " (Gen): ", l.StrName)
		fmt.Println()
	}
}

func CompareTwoMaps(map1 map[*StLockingOp]bool, map2 map[*StLockingOp]bool) bool {
	if len(map1) != len(map2) {
		return false
	}

	for s, _ := range map1 {
		if _, ok := map2[s]; !ok {
			return false
		}
	}

	return true
}

func GetLiveMutex(inputInst ssa.Instruction) map[*StLockingOp]bool {
	mapReturn := make(map[*StLockingOp]bool)

	for op, _ := range mapBefore[inputInst] {
		mapReturn[op] = true
	}

	return mapReturn
}

func InitGenKillMap(inputFn *ssa.Function) {

	mapGen = make(map[ssa.Instruction]*StLockingOp)
	mapKill = make(map[ssa.Instruction]*StUnlockingOp)

	for _, bb := range inputFn.Blocks {
		for _, ii := range bb.Instrs {
			if _, ok := mapIIStLockingOp[ii]; ok {
				mapGen[ii] = mapIIStLockingOp[ii]
			}

			if _, ok := mapIIStUnlockingOp[ii]; ok {
				mapKill[ii] = mapIIStUnlockingOp[ii]
			}
		}
	}
}

func InitBeforeAfterMap(inputFn *ssa.Function, contextLock map[*StLockingOp]bool) {
	for _, bb := range inputFn.Blocks {
		for _, ii := range bb.Instrs {
			m1 := make(map[*StLockingOp]bool)
			mapBefore[ii] = m1
			m2 := make(map[*StLockingOp]bool)
			mapAfter[ii] = m2
		}
	}

	vecEntryInsts := util.GetEntryInsts(inputFn)

	//fmt.Println("InitBeforeAfterMap:", inputFn.Name(), len(contextLock), len(vecEntryInsts))

	for _, ii := range vecEntryInsts {
		for l, _ := range contextLock {
			mapBefore[ii][l] = true
		}
	}
}

func UnionGenSet(newbefore map[*StLockingOp]bool, pLocking *StLockingOp) *StDoubleLock {

	//fmt.Println("inside union gen")

	for l, _ := range newbefore {

		if l.Parent == pLocking.Parent { // try alias analysis here
			bug := &StDoubleLock{
				PLock1: l,
				PLock2: pLocking,
			}

			//should we return here?
			return bug
		}
	}

	newbefore[pLocking] = true

	return nil
}

func KillKillSet(newbefore map[*StLockingOp]bool, pUnlocking *StUnlockingOp) {
	for l, _ := range newbefore {
		if l.Parent == pUnlocking.Parent {
			delete(newbefore, l)
			//return
		}
	}
}

func GenKillAnalysis(inputFn *ssa.Function, contextLock map[*StLockingOp]bool) []*StDoubleLock {

	//fmt.Println(inputFn.Name(), len(contextLock))

	mapGen = make(map[ssa.Instruction]*StLockingOp)
	mapKill = make(map[ssa.Instruction]*StUnlockingOp)
	mapBefore = make(map[ssa.Instruction]map[*StLockingOp]bool)
	mapAfter = make(map[ssa.Instruction]map[*StLockingOp]bool)

	bugs := make([]*StDoubleLock, 0)

	InitGenKillMap(inputFn)

	if len(mapGen) == 0 && len(mapKill) == 0 && len(contextLock) == 0 {
		return bugs
	}

	InitBeforeAfterMap(inputFn, contextLock)

	//printMapBefore()

	vecWorkList := make([]ssa.Instruction, 0)

	for _, bb := range inputFn.Blocks {
		for _, ii := range bb.Instrs {
			vecWorkList = append(vecWorkList, ii)
		}
	}

	for len(vecWorkList) > 0 {
		ii := vecWorkList[len(vecWorkList)-1]
		vecWorkList = vecWorkList[:len(vecWorkList)-1]

		prevIIs := util.GetPrevInsts(ii)

		newBefore := make(map[*StLockingOp]bool)

		if len(prevIIs) > 0 {
			for _, prevII := range prevIIs {
				for op, _ := range mapAfter[prevII] {
					newBefore[op] = true
				}
			}

			mapBefore[ii] = make(map[*StLockingOp]bool)

			for op, _ := range newBefore {
				mapBefore[ii][op] = true
			}
		} else {
			for op, _ := range mapBefore[ii] {
				newBefore[op] = true
			}
		}

		//if inputFn.Name() == "ProtectedInc" {
		//	fmt.Println(ii, len(newBefore))
		//}

		if op, ok := mapGen[ii]; ok {
			bug := UnionGenSet(newBefore, op)
			if bug != nil {
				bugs = append(bugs, bug)
			}
		}

		if op, ok := mapKill[ii]; ok {
			KillKillSet(newBefore, op)
		}

		//if op, ok := mapGen[ii]; ok {
		//	newBefore[op] = true
		//}

		//if strMutexName, ok := mapKill[ii]; ok {
		//	delete(newBefore, strMutexName)
		//
		//}

		if !CompareTwoMaps(newBefore, mapAfter[ii]) {
			mapAfter[ii] = newBefore
			for _, pI := range util.GetSuccInsts(ii) {
				vecWorkList = append(vecWorkList, pI)
			}
		}
	}

	return bugs
}

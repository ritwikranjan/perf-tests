package main

import (
	"fmt"
	"os"
)

// NetPerfRPC service that exposes RegisterClient and ReceiveOutput for clients
type NetPerfRPC int

func (t *NetPerfRPC) ReceiveOutput(data *WorkerOutput, _ *int) error {
	globalLock.Lock()
	defer globalLock.Unlock()

	testcase := testcases[data.TestCase]

	outputLog := fmt.Sprintln("Received output from worker", data.Worker, "for test", testcase.Label,
		"from", testcase.SourceNode, "to", testcase.DestinationNode) + data.Output
	writeOutputFile(outputCaptureFile, outputLog)

	if testcase.BandwidthParser != nil {
		bw, mss := testcase.BandwidthParser(data.Output)
		registerDataPoint(testcase.Label, mss, fmt.Sprintf("%f", bw), currentJobIndex)
		fmt.Println("Jobdone from worker", data.Worker, "Bandwidth was", bw, "Mbits/sec")
	}

	if testcase.JsonParser != nil {
		addResult(
			fmt.Sprintf("%s with MSS: %d", testcase.Label, testcase.MSS),
			testcase.JsonParser(data.Output),
		)
		fmt.Println("Jobdone from worker", data.Worker, "JSON output generated")
	}

	return nil
}

func (t *NetPerfRPC) RegisterClient(data ClientRegistrationData, workItem *WorkItem) error {
	globalLock.Lock()
	defer globalLock.Unlock()

	state, ok := workerStateMap[data.Worker]

	if !ok {
		// For new clients, trigger an iperf server start immediately
		state = &workerState{sentServerItem: true, idle: true, IP: data.IP, worker: data.Worker}
		workerStateMap[data.Worker] = state
		workItem.IsServerItem = true
		workItem.ServerWorkItem.ListenPort = "5201"
		workItem.ServerWorkItem.Timeout = 3600
		return nil
	}

	// Worker defaults to idle unless the allocateWork routine below assigns an item
	state.idle = true

	// Give the worker a new work item or let it idle loop another 5 seconds
	allocateWorkToClient(state, workItem)
	return nil
}

func allocateWorkToClient(workerState *workerState, workItem *WorkItem) {
	if !allWorkersIdle() {
		workItem.IsIdle = true
		return
	}

	// System is all idle - pick up next work item to allocate to client
	for n, v := range testcases {
		if v.Finished {
			continue
		}
		if v.SourceNode != workerState.worker {
			workItem.IsIdle = true
			return
		}
		if _, ok := workerStateMap[v.DestinationNode]; !ok {
			workItem.IsIdle = true
			return
		}
		fmt.Printf("Requesting jobrun '%s' from %s to %s for MSS %d for MsgSize %d\n", v.Label, v.SourceNode, v.DestinationNode, v.MSS, v.MsgSize)
		workItem.IsClientItem = true
		workItem.TestCase = n
		workItem.ClientWorkItem.TestParams = v.TestParams
		fmt.Println("workItem.TestParams: \n", workItem.TestParams)
		workerState.idle = false

		if !v.ClusterIP {
			workItem.Host = workerStateMap[workerState.worker].IP
		} else {
			workItem.Host = os.Getenv("NETPERF_W2_SERVICE_HOST")
		}

		if v.MSS != 0 && v.MSS < mssMax {
			v.MSS += mssStepSize
		} else {
			v.Finished = true
		}

		if v.Type == netperfTest {
			workItem.Port = "12865"
		} else {
			workItem.Port = "5201"
		}

		return
	}

	for _, v := range testcases {
		if !v.Finished {
			return
		}
	}

	if !datapointsFlushed {
		fmt.Println("ALL TESTCASES AND MSS RANGES COMPLETE - GENERATING CSV OUTPUT")
		flushDataPointsToCsv()
		flushResultJsonData()
		datapointsFlushed = true
	}

	workItem.IsIdle = true
}

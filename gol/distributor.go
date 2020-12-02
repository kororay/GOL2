package gol

import (
	"fmt"
	"sync"
	"time"

	"uk.ac.bris.cs/gameoflife/util"
)

type distributorChannels struct {
	events    chan<- Event
	ioCommand chan<- ioCommand
	ioIdle    <-chan bool

	filename chan<- string
	outputQ  chan<- uint8
	inputQ   <-chan uint8
}

// distributor divides the work between workers and interacts with other goroutines.
func distributor(p Params, c distributorChannels, keyPresses <-chan rune) {

	controllerFlag := make(chan int, 2)
	go controller(keyPresses, controllerFlag)

	// TODO: Create a 2D slice to store the world.
	var turn int // undecalered value is zero

	ticker := time.NewTicker(2 * time.Second) // create a ticker for our alivecellscount event anon func
	done := make(chan bool)                   // so we can end the anon go function for allivecellscount
	var mutex = &sync.Mutex{}

	initialWorld := make([][]uint8, p.ImageHeight) //make empty board heightxwidth
	for i := 0; i < (p.ImageHeight); i++ {
		initialWorld[i] = make([]uint8, p.ImageWidth)
	}

	//open the pgm file to game of life
	fileName := fmt.Sprintf("%vx%v", p.ImageWidth, p.ImageHeight)

	//read the game of life and convert the pgm into a slice of slices
	c.ioCommand <- ioInput
	c.filename <- fileName

	for i := 0; i < (p.ImageHeight); i++ { //take the bytes we get from the inputQ channel and populate the empty board
		for j := 0; j < (p.ImageHeight); j++ {
			initialWorld[i][j] = <-c.inputQ
		}
	}

	// TODO: For all initially alive cells send a CellFlipped Event.
	for _, cellQ := range calculateAliveCells(p, initialWorld) {
		c.events <- CellFlipped{0, cellQ}
	}

	// TODO: Execute all turns of the Game of Life.
	var state State

	sliceOfCh := make([]chan [][]uint8, p.Threads) // make a separate channel for each divided piece of the game board

	world := initialWorld

	go func() { //anon function that reports alivecellcount every two seconds
		for {
			select {
			case <-ticker.C:
				mutex.Lock() //mutex as this cant happen at the same times lines as lines 107-108
				c.events <- AliveCellsCount{turn, len(calculateAliveCells(p, world))}
				mutex.Unlock()

			case <-done:
				return

			}
		}
	}()

	for turnf := 0; turnf < p.Turns; turnf++ {

		baseLines := p.ImageHeight / p.Threads
		slackLines := p.ImageHeight % p.Threads // remainder
		workLines := make([]int, p.Threads)

		for i := 0; i < p.Threads; i++ { // create an array with the minimum amount of lines to work on
			workLines[i] = baseLines
		}

		for i := 0; i < slackLines; i++ { // adds the remainder to the array
			workLines[i]++
		}

		for i := 0; i < p.Threads; i++ { //call a worker for each section we are splitting the board into
			sliceOfCh[i] = make(chan [][]uint8)

			go worker(workLines, i, world, sliceOfCh[i], p, c, turnf)
		}

		var newData [][]uint8

		for i := 0; i < p.Threads; i++ { // take our updated parts and put them back togther
			slice := <-sliceOfCh[i]
			newData = append(newData, slice...)
		}

		mutex.Lock()
		world = newData //update the board state
		turn++          //increment the turn counter

		mutex.Unlock()

		state = 1                      //set the state to exectuing
		c.events <- TurnComplete{turn} //send turn complete event

		//fmt.Println(<-keyPresses)
		var keyFlag int
		controllerFlag <- 4
		keyFlag = <-controllerFlag

		if keyFlag == 2 { // when q is pressed, quit turn
			state = 2
			c.events <- StateChange{turn, state}
			<-controllerFlag // remove 4 from the buffer
			break
		}
		if keyFlag == 0 { // when p is pressed, pause turn
			state = 0
			<-controllerFlag
			c.events <- StateChange{turn, state}
			for {
				keyFlag = <-controllerFlag
				if keyFlag == 0 { // when p is pressed again, resume
					state = 1
					fmt.Println("Continuing")
					c.events <- StateChange{turn, state}
					break
				}
			}
		}
		if keyFlag == 1 { // when s is pressed, print current turn
			outName := fmt.Sprintf("%vx%vx%v", p.ImageWidth, p.ImageHeight, turn)

			c.ioCommand <- ioOutput
			c.filename <- outName

			for i := 0; i < (p.ImageHeight); i++ {
				for j := 0; j < (p.ImageHeight); j++ {
					c.outputQ <- world[i][j]
				}
			}
			c.events <- ImageOutputComplete{turn, outName}
			<-controllerFlag

		}
		//c.events <- StateChange{turnf, state}--------------------------------------------------------------remove this line
	}

	// TODO: Send correct Events when required, e.g. CellFlipped, TurnComplete and FinalTurnComplete.
	//		 See event.go for a list of all events.

	c.events <- FinalTurnComplete{turn, calculateAliveCells(p, world)}

	ticker.Stop() //stop ticker
	done <- true  // send fl;ag to finish anon go routine for alivecellscount

	// output state of game as PGM after all turns completed
	outName := fmt.Sprintf("%vx%vx%v", p.ImageWidth, p.ImageHeight, turn)

	c.ioCommand <- ioOutput
	c.filename <- outName

	for i := 0; i < (p.ImageHeight); i++ {
		for j := 0; j < (p.ImageHeight); j++ {
			c.outputQ <- world[i][j]
		}
	}

	c.events <- ImageOutputComplete{turn, outName}

	// Make sure that the Io has finished any output before exiting.
	c.ioCommand <- ioCheckIdle
	<-c.ioIdle

	c.events <- StateChange{turn, Quitting}
	// Close the channel to stop the SDL goroutine gracefully. Removing may cause deadlock.
	close(c.events)
}

func calculateAliveCells(p Params, world [][]uint8) []util.Cell {
	aliveCells := []util.Cell{}
	for y := 0; y < p.ImageHeight; y++ {
		for x := 0; x < p.ImageWidth; x++ {
			if world[y][x] == 255 {
				aliveCells = append(aliveCells, util.Cell{X: x, Y: y})
			}
		}
	}
	return aliveCells
}

func worker(lines []int, sliceNum int, world [][]uint8, sliceOfChi chan<- [][]uint8, p Params, c distributorChannels, turnf int) {
	var worldGo [][]uint8
	var newData [][]uint8

	compLines := 0

	for i := 0; i < sliceNum; i++ {
		compLines = compLines + lines[i]
	}

	if sliceNum == 0 { // is this the first slice of the GOL board
		newData = append(newData, world[p.ImageHeight-1]) // add last line to start
		if p.Threads == 1 {                               //is this the only slice
			worldGo = world
			worldGo = append(worldGo, world[0]) // add first line to the end if we have 1 worker
		} else {
			worldGo = world[:lines[sliceNum]+1]
		}

		worldGo = append(newData, worldGo...)

	} else if sliceNum == p.Threads-1 { //is this neither the first or last slice of the GOL board
		worldGo = world[compLines-1:]
		worldGo = append(worldGo, world[0])
	} else { //is this the last slice of the GOL board
		worldGo = world[compLines-1 : compLines+lines[sliceNum]+1]
	}

	part := calculateNextState(p, worldGo, c, turnf, compLines)

	sliceOfChi <- part
}

func calculateNextState(p Params, world [][]uint8, c distributorChannels, turnf int, compLines int) [][]uint8 {

	var counter, nextX, lastX, nextY, lastY int //can I use a byte here instead
	newWS := make([][]uint8, (len(world) - 2))
	for i := 0; i < (len(world) - 2); i++ {
		newWS[i] = make([]uint8, p.ImageWidth)
	}

	tempW := world[1 : len(world)-1]

	for y, s := range tempW {

		for x, sl := range s {

			counter = 0

			//Set the nextX and lastX variables
			if x == len(s)-1 { //are we looking at the last element of the slice
				nextX = 0
				lastX = x - 1
			} else if x == 0 { //are we looking at the first element of the slice
				nextX = x + 1
				lastX = len(s) - 1
			} else { //we are looking at any element that is not the first of last element of a slice
				nextX = x + 1
				lastX = x - 1
			}

			//Set the nextY and lastY variables
			nextY = y + 2
			lastY = y

			if 255 == s[nextX] {
				counter++
			} //look E
			if 255 == s[lastX] {
				counter++
			} //look W

			if 255 == world[lastY][lastX] {
				counter++
			} //look NW
			if 255 == world[lastY][x] {
				counter++
			} //look N
			if 255 == world[lastY][nextX] {
				counter++
			} //look NE

			if 255 == world[nextY][lastX] {
				counter++
			} //look SW
			if 255 == world[nextY][x] {
				counter++
			} //look S
			if 255 == world[nextY][nextX] {
				counter++
			} //look SE

			//Live cells
			if sl == 255 {
				if counter < 2 || counter > 3 { //"any live cell with fewer than two or more than three live neighbours dies"
					newWS[y][x] = 0
					c.events <- CellFlipped{turnf, util.Cell{X: x, Y: (y + compLines)}}

				} else { //"any live cell with two or three live neighbours is unaffected"
					newWS[y][x] = 255
				}

			}

			//Dead cell -- not MGS
			if sl == 0 {
				if counter == 3 { //"any dead cell with exactly three live neighbours becomes alive"
					newWS[y][x] = 255
					c.events <- CellFlipped{turnf, util.Cell{X: x, Y: (y + compLines)}}

				} else {
					newWS[y][x] = 0 // Dead cells elsewise stay dead
				}

			}

		}

	}

	return newWS

}

func controller(keyPresses <-chan rune, controllerFlag chan<- int) {
	for {
		keypressed := <-keyPresses
		switch {
		case keypressed == 113: // when q is pressed
			controllerFlag <- 2
		case keypressed == 112: // when p is pressed
			controllerFlag <- 0
		case keypressed == 115: // when s is pressed
			controllerFlag <- 1

		}

	}

}

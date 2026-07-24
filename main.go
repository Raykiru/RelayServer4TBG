package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

const AUTH = true
const TIMEOUT_DUR = 300 // 5 minutes

func todo(msg string) {
	log.Print("TODO: ", msg)
	panic("")
}

type Player struct {
	Mutex *sync.Mutex
	Name  string
	Comm  chan []byte
}

func host_handshake(room *Room, timeout <-chan time.Time) int {
	// step 2a1: Host waits for guest's messege
	select {
	case <-room.host_ch:
	case <-room.quit:
		return http.StatusGone
	case <-timeout:
		return http.StatusGatewayTimeout
	}
	// endof step 2a1

	// step 2a2: Host notifies guest that it recieved messege
	select {
	case room.guest_ch <- true:
	case <-room.quit:
		return (http.StatusGone)
	case <-timeout:
		return (http.StatusGatewayTimeout)
	}
	// endof step 2a2
	return 0
}

func guest_handshake(room *Room, timeout <-chan time.Time) int {
	// step 2b1: Guest sends messege to host
	select {
	case room.host_ch <- true:
	case <-room.quit:
		return (http.StatusGone)

	case <-timeout:
		return (http.StatusGatewayTimeout)

	}
	// endof step 2b1

	// step 2b2: Recieve the acknowledgement from host
	select {
	case <-room.guest_ch:
	case <-room.quit:
		return (http.StatusGone)

	case <-timeout:
		return (http.StatusGatewayTimeout)

	}
	// endof step 2b2
	return 0
}

type Room struct {
	Host      Player
	Guest     Player
	quit      chan struct{}
	quit_once sync.Once
	host_ch   chan bool
	guest_ch  chan bool
}

type Store struct {
	Room_mutex   *sync.RWMutex
	Rooms        map[int]*Room
	SRoom_mutex  *sync.RWMutex
	Started_room map[int]*Room
	Counter      int
}

var store Store

var internal_log string

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080" // fallback for local development
	}
	defer http.ListenAndServe(":"+port, nil)

	Logger_init()

	store = Store{
		new(sync.RWMutex),
		make(map[int]*Room),
		new(sync.RWMutex),
		make(map[int]*Room),
		0,
	}

	// serve the frontend
	http.Handle("/", http.FileServer(http.Dir("../docs/")))

	http.HandleFunc("/logs", Log_buffer)

	http.HandleFunc("POST /token", Token)

	/*
		Game related endpoints
	*/
	{
		// end/leave a game
		http.HandleFunc("DELETE /game/{role}/{id}", func(w http.ResponseWriter, r *http.Request) {

			role := r.PathValue("role")

			id_string := r.PathValue("id")
			id, err := strconv.Atoi(id_string)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, "Error parsing room id:", err)
				log.Print("Error parsing room id:", err)
				return
			}

			defer log.Printf("(/%v %v) game/%v/%v", r.Method, r.Host, role, id)

			if AUTH {
				var room *Room
				var exists bool
				store.SRoom_mutex.RLock()
				{
					room, exists = store.Started_room[id]
				}
				store.SRoom_mutex.RUnlock()
				if !exists {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				is_host := role == "HOST"

				jwt_claims := ExtractClaims(r)
				if jwt_claims == nil {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}

				if VerifyAdmin(jwt_claims) {
					goto auth_end
				}

				if is_host {
					if !Verify(jwt_claims, "HOST", room.Host.Name, id) {
						w.WriteHeader(http.StatusForbidden)
						return
					}
				} else {
					if !Verify(jwt_claims, "GUEST", room.Guest.Name, id) {
						w.WriteHeader(http.StatusForbidden)
						return
					}
				}
			}

		auth_end:

			store.SRoom_mutex.Lock()
			defer store.SRoom_mutex.Unlock()

			room, ok := store.Started_room[id]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}

			room.quit_once.Do(func() { close(room.quit) })

			delete(store.Started_room, id)

			w.WriteHeader(http.StatusOK)

		})
		// start a game
		http.HandleFunc("GET /game/start/{role}/{id}", func(w http.ResponseWriter, r *http.Request) {
			// step 0: get credentials
			role := r.PathValue("role")

			id_string := r.PathValue("id")
			id, err := strconv.Atoi(id_string)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, "Error parsing room id:", err)
				log.Print("Error parsing room id:", err)
				return
			}
			// endof step 0

			defer log.Printf("(/%v %v) game/start/%v/%v", r.Method, r.Host, role, id)

			// step 1: get information about the room
			var room *Room
			var exists bool
			store.Room_mutex.RLock()
			{
				room, exists = store.Rooms[id]
			}
			store.Room_mutex.RUnlock()

			// check if the room was actually there
			if !exists {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			fmt.Println("Got room at id", id)

			var player Player
			var is_host bool = false
			switch role {
			case "HOST":
				is_host, player = true, room.Host
			case "GUEST":
				player = room.Guest
			default:
				w.WriteHeader(http.StatusForbidden)
				return
			}
			// endof step 1

			// step 2: authorise
			if AUTH { // verify jwt token
				jwt_claims := ExtractClaims(r)
				if jwt_claims == nil {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}

				if is_host {
					if !Verify(jwt_claims, "HOST", room.Host.Name, id) {
						w.WriteHeader(http.StatusForbidden)
						return
					}
				} else {
					if !Verify(jwt_claims, "GUEST", room.Guest.Name, id) {
						w.WriteHeader(http.StatusForbidden)
						return
					}
				}

				// else, free to go
			}
			// endof step 2

			// step 3: wait for player randevouz, if timeout, abort operation
			timeout := time.After(TIMEOUT_DUR * time.Second)

			var response int
			if is_host {
				response = host_handshake(room, timeout)

			} else {
				response = guest_handshake(room, timeout)
			}
			if response != 0 {
				fmt.Print("handshake response:", response)
				w.WriteHeader(response)
				return
			}
			// endof step 3

			// step 4: move the room in the started rooms section if the host sent the request
			if is_host {
				store.Room_mutex.Lock()
				{
					delete(store.Rooms, id)
				}
				store.Room_mutex.Unlock()

				store.SRoom_mutex.Lock()
				{
					store.Started_room[id] = room
				}
				store.SRoom_mutex.Unlock()
			}
			// endof step 4

			// step 5: initialise game state with default values
			// endof step 5

			// step 6: send both player current gamestate
			json.NewEncoder(w).Encode(player)
			// endof step 6
		})

		// request the current gamestate of a player
		http.HandleFunc("GET /game/{role}/{room_id}", func(w http.ResponseWriter, r *http.Request) {
			// step 0: retrieve room information
			room_id_string := r.PathValue("room_id")
			room_id, err := strconv.Atoi(room_id_string)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, "Room_id must be a valid integer")
				return
			}
			role := r.PathValue("role")

			defer log.Printf("(/%v %v) game/%v/%v", r.Method, r.Host, role, room_id)

			var room *Room
			var exists bool
			store.SRoom_mutex.RLock()
			{
				room, exists = store.Started_room[room_id]
			}
			store.SRoom_mutex.RUnlock()

			// theres a small chance the guest has sent an action before the room was moved to started rooms
			if !exists {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			// endof step 0

			// step 1: retrive player information
			var player Player
			var opponent Player
			switch role {
			case "HOST":
				player = room.Host
				opponent = room.Guest
			case "GUEST":
				player = room.Guest
				opponent = room.Host
			default:
				{
					w.WriteHeader(http.StatusBadRequest)
					fmt.Fprintf(w, "Role [%v] doesn't exist, use [host] or [guest]", role)
					return
				}
			}

			if AUTH { // verify player id via jwt
				jwt_claims := ExtractClaims(r)
				if jwt_claims == nil {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}

				if !Verify(jwt_claims, role, player.Name, room_id) {
					w.WriteHeader(http.StatusForbidden)
					return
				}

				// else, free to go
			}
			// endof step 1

			// step 6: package the new gamestate and send it

			// to make sure I read opponents gamestate only after they've finished updating theirs
			opponent.Mutex.Lock()
			{
				// result.Op_gamestate = format_op_gamestate(opponent.gamestate, Card{})
			}
			opponent.Mutex.Unlock()
			// endof step 6

			// step 7: send the result
			w.WriteHeader(http.StatusOK)
			// json.NewEncoder(w).Encode(result)
			// endof step 7
		})
		// register actions/commands from players and returns state of the game after action
		http.HandleFunc("POST /game/{role}/{room_id}", func(w http.ResponseWriter, r *http.Request) {
			// step 0: retrieve room information
			room_id_string := r.PathValue("room_id")
			room_id, err := strconv.Atoi(room_id_string)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, "Room_id must be a valid integer")
				return
			}
			role := r.PathValue("role")

			defer log.Printf("(/%v %v) game/%v/%v", r.Method, r.Host, role, room_id)

			var room *Room
			var exists bool
			store.SRoom_mutex.RLock()
			{
				room, exists = store.Started_room[room_id]
			}
			store.SRoom_mutex.RUnlock()

			// theres a small chance the guest has sent an action before the room was moved to started rooms
			if !exists {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			// endof step 0

			// step 1: retrive player information
			var player Player
			var opponent Player
			switch role {
			case "HOST":
				player = room.Host
				opponent = room.Guest
			case "GUEST":
				player = room.Guest
				opponent = room.Host
			default:
				{
					w.WriteHeader(http.StatusBadRequest)
					fmt.Fprintf(w, "Role [%v] doesn't exist, use [host] or [guest]", role)
					return
				}
			}
			_ = opponent

			if AUTH { // verify player id via jwt
				jwt_claims := ExtractClaims(r)
				if jwt_claims == nil {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}

				if !Verify(jwt_claims, role, player.Name, room_id) {
					w.WriteHeader(http.StatusForbidden)
					return
				}

				// else, free to go
			}
			// endof step 1

			// step 1a: if player asked to change the dungeon, do that and imediately return the new dungeon
			// endof step 1a

			// step 2 : synchronise players actions; if timeout abort action
			// endof step 2

			// step 3: execute active player action

			// endof step 3

			// step 3a: if the action failed, close the room
			// endof step 3a

			// step 4: attack defending player

			// TODO: implement relaying the data between players
			// select {
			// case opponent.Comm <- selected_card:
			// default:
			// 	// if sent too many commands in the row
			// 	w.WriteHeader(http.StatusTooManyRequests)
			// 	return
			// }

			// endof step 4

			// step 5 : resolve opponents attack

			// now that I finished updating my gamestate after sending my op_card, I can safely unlock
			// endof step 5

			// step 6: package the new gamestate and send it

			// to make sure I read opponents gamestate only after they've finished updating theirs

			// endof step 6

			// step 6a: check if the game didn't conclude, if it did,send signal to close the room
			// endof step 6a

			// step 7: send the result
			w.WriteHeader(http.StatusOK)
			// json.NewEncoder(w).Encode(result)
			// endof step 7
		})
	}

	/*
		Room related endpoints
	*/
	{
		// create a new room with self as the host, returns room id as response on OK
		http.HandleFunc("POST /room/{host}", func(w http.ResponseWriter, r *http.Request) {
			host := r.PathValue("host")

			defer log.Printf("(/%v %v) room/%v", r.Method, r.Host, host)

			if AUTH { // verify jwt token
				jwt_claims := ExtractClaims(r)
				if jwt_claims == nil {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}

				if VerifyAdmin(jwt_claims) {
					goto auth_end
				}
				if !Verify(jwt_claims, "HOST", host, 0) {
					w.WriteHeader(http.StatusForbidden)
					return
				}

				// else, free to go
			}
		auth_end:

			if host == "" {
				w.WriteHeader(http.StatusBadRequest)

				fmt.Fprint(w, "Empty username not allowed when craeting new room")
				return
			}

			new_room := new(Room{
				Player{},
				Player{},
				make(chan struct{}),
				sync.Once{},
				make(chan bool),
				make(chan bool),
			})
			new_room.Host = Player{
				new(sync.Mutex),
				host,
				make(chan []byte, 1),
			}

			var new_room_id int

			store.Room_mutex.Lock()
			{
				store.Rooms[store.Counter] = new_room
				new_room_id = store.Counter
				store.Counter += 1
			}
			store.Room_mutex.Unlock()

			fmt.Fprintf(w, `{"room_id": %v}`, new_room_id)
		})

		// Get an update on the room
		http.HandleFunc("GET /room/{id}", func(w http.ResponseWriter, r *http.Request) {
			id_string := r.PathValue("id")
			id, err := strconv.Atoi(id_string)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, "Error parsing room id:", err)
				log.Print("Error parsing room id:", err)
				return
			}

			defer log.Printf("(/%v %v) /room/%v", r.Method, r.Host, id)

			var room *Room
			var exists bool
			store.Room_mutex.RLock()
			{
				room, exists = store.Rooms[id]
			}
			store.Room_mutex.RUnlock()

			// check if the room was actually there
			if !exists {
				w.WriteHeader(http.StatusNotFound)
				return
			}

			if AUTH { // verify jwt token
				jwt_claims := ExtractClaims(r)
				if jwt_claims == nil {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}

				if VerifyAdmin(jwt_claims) {
					goto auth_end
				}

				if !Verify(jwt_claims, "HOST", room.Host.Name, id) &&
					!Verify(jwt_claims, "GUEST", room.Guest.Name, id) {
					w.WriteHeader(http.StatusForbidden)
					return
				}

			}

		auth_end:
			store.Room_mutex.RLock()
			Room_json, _ := json.Marshal(store.Rooms[id])
			store.Room_mutex.RUnlock()

			w.Write(Room_json)
		})
		// get list of all rooms as a list [{host: <s>, guest:<s>}, ...] as json
		http.HandleFunc("GET /room", func(w http.ResponseWriter, r *http.Request) {
			defer log.Printf("(/%v %v) /room", r.Method, r.Host)

			if AUTH { // verify jwt token
				jwt_claims := ExtractClaims(r)
				if jwt_claims == nil {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}

				role, exists := jwt_claims["role"]
				if !exists {
					w.WriteHeader(http.StatusBadRequest)
					return
				}

				if role != "VIEWER" && role != "ADMIN" {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				// else, free to go
			}
			store.Room_mutex.RLock()
			Rooms_json, _ := json.Marshal(store.Rooms)
			store.Room_mutex.RUnlock()

			w.Write(Rooms_json)
		})

		// join a room, returns { room_id : <int> } json as response on OK
		http.HandleFunc("PATCH /room/{guest}/{id}", func(w http.ResponseWriter, r *http.Request) {
			guest := r.PathValue("guest")
			id_string := r.PathValue("id")
			id, err := strconv.Atoi(id_string)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, "Error parsing room id:", err)
				log.Print("Error parsing room id:", err)
				return
			}

			defer log.Printf("(/%v %v) /room/%v/%v", r.Method, r.Host, guest, id)

			if AUTH { // verify jwt token
				jwt_claims := ExtractClaims(r)
				if jwt_claims == nil {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}

				if VerifyAdmin(jwt_claims) {
					goto end_auth
				}

				if !Verify(jwt_claims, "GUEST", guest, id) {
					w.WriteHeader(http.StatusForbidden)
					return
				}
			}

		end_auth:
			store.Room_mutex.Lock()
			defer store.Room_mutex.Unlock()

			target_room, exists := store.Rooms[id]
			if !exists {
				w.WriteHeader(http.StatusNotFound)
				return
			}

			target_room.Guest = Player{
				new(sync.Mutex),
				guest,
				make(chan []byte, 1),
			}

			store.Rooms[id] = target_room

			target_room_json, _ := json.Marshal(target_room)

			w.Write(target_room_json)

		})

		http.HandleFunc("DELETE /room/{guest}/{id}", func(w http.ResponseWriter, r *http.Request) {
			id_string := r.PathValue("id")
			id, err := strconv.Atoi(id_string)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, "Error parsing room id:", err)
				log.Print("Error parsing room id:", err)
				return
			}
			guest := r.PathValue("guest")
			defer log.Printf("(/%v %v) /room/%v/%v", r.Method, r.Host, guest, id)

			var old_room *Room
			var exists bool
			store.Room_mutex.RLock()
			{
				old_room, exists = store.Rooms[id]
			}
			store.Room_mutex.RUnlock()
			if !exists {
				w.WriteHeader(http.StatusNotFound)
				return
			}

			if old_room.Guest.Name != guest {
				w.WriteHeader(http.StatusForbidden)
				return
			}

			if AUTH { // verify jwt token and extract info

				jwt_claims := ExtractClaims(r)
				if jwt_claims == nil {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}

				if VerifyAdmin(jwt_claims) {
					goto delete_guest
				}

				// only the host and the guest themselves has the authority to remove the guest from the room
				if !Verify(jwt_claims, "GUEST", old_room.Guest.Name, id) && !Verify(jwt_claims, "HOST", old_room.Host.Name, id) {
					w.WriteHeader(http.StatusForbidden)
					return
				}

			}

		delete_guest:
			old_room.Guest.Name = ""

			store.Room_mutex.Lock()
			store.Rooms[id] = old_room
			store.Room_mutex.Unlock()

			w.WriteHeader(http.StatusOK)

		})

		http.HandleFunc("DELETE /room/{id}", func(w http.ResponseWriter, r *http.Request) {
			id_string := r.PathValue("id")
			id, err := strconv.Atoi(id_string)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, "Error parsing room id:", err)
				log.Print("Error parsing room id:", err)
				return
			}
			defer log.Printf("(/%v %v) /room/%v", r.Method, r.Host, id)

			var old_room *Room
			var exists bool
			store.Room_mutex.RLock()
			{
				old_room, exists = store.Rooms[id]
			}
			store.Room_mutex.RUnlock()
			if !exists {
				w.WriteHeader(http.StatusNotFound)
				return
			}

			var is_admin bool
			if AUTH { // verify jwt token and extract info

				jwt_claims := ExtractClaims(r)
				if jwt_claims == nil {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}

				is_admin = VerifyAdmin(jwt_claims)
				if is_admin {
					goto delete_room
				}

				if !Verify(jwt_claims, "HOST", old_room.Host.Name, id) {
					w.WriteHeader(http.StatusForbidden)
					return
				}

			}

		delete_room:
			store.Room_mutex.Lock()
			delete(store.Rooms, id)
			store.Room_mutex.Unlock()

			old_room_json, _ := json.Marshal(old_room)
			w.Write(old_room_json)

		})
	}

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("(/%v %v) Health", r.Method, r.Host)
		w.WriteHeader(http.StatusOK)
	})

}

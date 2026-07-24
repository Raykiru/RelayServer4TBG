# Relay Server 4TBG 
RS4TBG is a relay server designed for quickly prototyping turn based games and getting them online.

## Usage
Build and deploymet:
(optional) Set `PORT` environment variable to ovewrite the default 8080 port
```
go build
server 
```

Deploy the server on your own machine or some other hosting service, make a game that uses it's api
and play the game over the network the server has access to.

## API
TBD

## Design goals
1. Simple api
2. Homogenous, allows multiple different games to be played on the same server
3. Provide a simple lobby system 
4. Enforce the order of turns(meaning, a player cannot make 2 "moves" at the same time, and if they do, the order should be handled correctly)
5. Reliable, fast, efficient 

## Anti-goals
1. Preventing cheating (it's for prototyping,not for competitive games)
2. Preventing mallware (if you download software from the internet without checking, that's on you)

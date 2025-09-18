package main


func main() {
	go tcp()
	go udp()
	select {}
}
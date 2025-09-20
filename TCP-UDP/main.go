package main

import "fmt"

func main() {
	var selectProtocol string
	fmt.Println("enter a protocol tcp or udp :")
	_,err :=fmt.Scan(&selectProtocol)
	if err !=nil{
		fmt.Println("error in scan")
		return
	}
	switch selectProtocol {
	case "tcp": tcp()
	case "udp": udp()
	default: fmt.Println("select right protocol")
	}
}

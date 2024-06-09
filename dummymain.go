package main

import (
	"fmt"
	"strings"

	"github.com/beevik/etree"
)

func dummymain() {
	xmlStr := `<?xml version="1.0" encoding="UTF-8"?>
	<cont:contact xmlns:cont="http://sssit.org/contact-us">
	<cont:name>Vimal Jaiswal</cont:name>
	<cont:company>SSSIT.org</cont:company>
	<cont:phone>(0120) 425-6464</cont:phone>
	</cont:contact>`

	// Parse the XML document
	doc := etree.NewDocument()
	if err := doc.ReadFromString(xmlStr); err != nil {
		panic(err)
	}

	// Find and remove elements
	removeElements(doc.Root(), "cont", "company")

	// Serialize the modified XML document
	result, err := doc.WriteToString()
	if err != nil {
		panic(err)
	}

	// Remove empty lines from the result
	result = removeEmptyLines(result)

	fmt.Println(result)
}

func removeElements(elem *etree.Element, namespace, tag string) {
	for _, child := range elem.ChildElements() {
		if child.Space == namespace && child.Tag == tag {
			elem.RemoveChild(child)
		} else {
			removeElements(child, namespace, tag)
		}
	}
	// Remove whitespace-only text nodes
	for _, child := range elem.Child {
		if c, ok := child.(*etree.CharData); ok && strings.TrimSpace(c.Data) == "" {
			elem.RemoveChild(child)
		}
	}
}

func removeEmptyLines(s string) string {
	lines := strings.Split(s, "\n")
	var result []string
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			result = append(result, line)
		}
	}
	return strings.Join(result, "\n")
}

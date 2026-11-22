package sshcommand

import (
	"errors"
	"strings"
	"unicode"
)

func shellWords(command string) ([]string, error) {
	command = strings.ReplaceAll(command, "\r\n", "\n")
	characters := []rune(strings.TrimSpace(command))
	var words []string
	var word strings.Builder
	var quote rune
	started := false
	for index := 0; index < len(characters); index++ {
		character := characters[index]
		if character == '\\' && quote != '\'' {
			if end := continuationEnd(characters, index+1); end != -1 {
				index = end
				continue
			}
			if index+1 == len(characters) {
				return nil, errors.New("SSH 命令末尾有未完成的转义。")
			}
			next := characters[index+1]
			if unicode.IsControl(next) {
				return nil, errors.New("SSH 命令包含不支持的控制字符。")
			}
			if quote == 0 || strings.ContainsRune("\\\"$`", next) {
				word.WriteRune(next)
				started = true
				index++
				continue
			}
		}

		if character == '\n' || character == '\r' {
			return nil, errors.New("请粘贴一条 SSH 命令。多行参数需要使用反斜杠续行。")
		}
		if quote != 0 && character == quote {
			quote = 0
			continue
		}
		if quote == 0 {
			if unicode.IsSpace(character) {
				if started {
					words = append(words, word.String())
					word.Reset()
					started = false
				}
				continue
			}
			if character == '\'' || character == '"' {
				quote = character
				started = true
				continue
			}
			if strings.ContainsRune(";&|<>(){}#", character) {
				return nil, errors.New("SSH 命令只能包含静态参数，不能包含 Shell 运算符。")
			}
		}
		if quote != '\'' && (character == '$' || character == '`') {
			return nil, errors.New("SSH 命令只能包含静态参数，不能包含变量或命令替换。")
		}
		if unicode.IsControl(character) {
			return nil, errors.New("SSH 命令包含不支持的控制字符。")
		}
		word.WriteRune(character)
		started = true
	}
	if quote != 0 {
		return nil, errors.New("SSH 命令中有未闭合的引号。")
	}
	if started {
		words = append(words, word.String())
	}
	return words, nil
}

func continuationEnd(characters []rune, next int) int {
	// Chat may show a doubled backslash where the shell uses a single one.
	if next < len(characters) && characters[next] == '\\' {
		next++
	}
	for next < len(characters) && characters[next] != '\n' && unicode.IsSpace(characters[next]) {
		next++
	}
	if next < len(characters) && characters[next] == '\n' {
		return next
	}
	return -1
}

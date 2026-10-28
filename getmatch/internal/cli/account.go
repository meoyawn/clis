package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/meoyawn/clis/getmatch/internal/domain"
	"github.com/meoyawn/clis/getmatch/internal/service"
)

func privateJSON(path string, value any, out io.Writer) error {
	if path == "" {
		return json.NewEncoder(out).Encode(value)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(map[string]any{"output": path})
}

func readJSON(path string, result any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 16<<20))
	d.DisallowUnknownFields()
	if err = d.Decode(result); err != nil {
		return err
	}
	if err = d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("unexpected trailing JSON in %q", path)
	}
	return nil
}

func runAccount(ctx context.Context, args []string, anonymous bool, out io.Writer) error {
	f := flag.NewFlagSet(args[0]+" "+args[1], flag.ContinueOnError)
	f.SetOutput(io.Discard)
	idValue := f.String("id", "", "vacancy ID or Getmatch URL")
	output := f.String("output", "", "new private JSON output file")
	cv := f.String("cv", "", "reviewed PDF file")
	letterFile := f.String("letter-file", "", "cover letter UTF-8 file")
	answersFile := f.String("answers-file", "", "reviewed answers JSON")
	draftFile := f.String("draft", "", "prepared application JSON")
	receiptFile := f.String("receipt", "", "saved submission receipt JSON")
	send := f.Bool("send", false, "submit the actual reviewed application once")
	reviewed := f.String("reviewed", "", "review package checksum")
	offset := f.Int("offset", 0, "application list offset")
	all := f.Bool("all", false, "read every application page")
	patchFile := f.String("patch", "", "shared-profile patch JSON")
	apply := f.Bool("apply", false, "save the explicitly requested shared-profile edit")
	backupDir := f.String("backup-dir", "", "private profile backup directory")
	if err := f.Parse(args[2:]); err != nil {
		return err
	}
	if f.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %v", f.Args())
	}
	allowed := map[string]bool{"output": true}
	switch args[0] + " " + args[1] {
	case "vacancies get", "application status":
		allowed["id"] = true
	case "application prepare":
		for _, name := range []string{"id", "cv", "letter-file", "answers-file"} {
			allowed[name] = true
		}
	case "application submit":
		for _, name := range []string{"draft", "send", "reviewed"} {
			allowed[name] = true
		}
	case "application verify":
		allowed["draft"] = true
		allowed["receipt"] = true
	case "profile update":
		allowed["patch"] = true
		allowed["apply"] = true
		allowed["backup-dir"] = true
	case "applications list":
		allowed["offset"] = true
		allowed["all"] = true
	}
	var bad string
	f.Visit(func(v *flag.Flag) {
		if !allowed[v.Name] {
			bad = v.Name
		}
	})
	if bad != "" {
		return fmt.Errorf("flag --%s does not belong to %s %s", bad, args[0], args[1])
	}
	if *output != "" {
		if _, err := os.Stat(*output); err == nil || !os.IsNotExist(err) {
			return fmt.Errorf("output %q must be a new private file", *output)
		}
	}
	if args[0] == "profile" && args[1] != "update" && *output == "" {
		return fmt.Errorf("profile %s requires --output for a private export", args[1])
	}
	if *cv != "" {
		return fmt.Errorf("--cv is disabled: Getmatch PDF uploads overwrite the shared profile; prepare a native-profile application")
	}
	var draft service.ApplicationDraft
	if args[0] == "application" && (args[1] == "submit" || args[1] == "verify") {
		if err := readJSON(*draftFile, &draft); err != nil {
			return err
		}
		if args[1] == "submit" && !*send {
			return privateJSON(*output, draft, out)
		}
		if args[1] == "submit" && *reviewed == "" {
			return fmt.Errorf("--send requires --reviewed SHA256 and prior user authorization")
		}
	}
	if anonymous {
		return fmt.Errorf("account commands require the saved signed-in session; --anonymous is unsupported")
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	dir = filepath.Join(dir, "getmatch")
	lock, err := os.OpenFile(filepath.Join(dir, "command.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("another getmatch command is active; wait for it to finish")
	}
	session, err := service.LoadSession(filepath.Join(dir, "session.json"))
	if err != nil {
		return err
	}
	httpClient, headers, err := service.HTTPClient(session)
	if err != nil {
		return err
	}
	defer httpClient.CloseIdleConnections()
	c := service.NewClient(httpClient, headers)
	var result any
	switch args[0] + " " + args[1] {
	case "profile get":
		result, err = c.Profile(ctx)
	case "profile update":
		var patch service.Object
		if err = readJSON(*patchFile, &patch); err != nil {
			return err
		}
		if *backupDir == "" {
			*backupDir = filepath.Join(dir, "backups")
		}
		result, err = c.UpdateProfile(ctx, patch, *apply, *backupDir)
	case "profile cv":
		var data []byte
		var hash string
		data, hash, err = c.ProfileCV(ctx)
		if err != nil {
			return err
		}
		var file *os.File
		file, err = os.OpenFile(*output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, err = file.Write(data)
		closeErr := file.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		return json.NewEncoder(out).Encode(map[string]any{"output": *output, "bytes": len(data), "sha256": hash})
	case "applications list":
		if *all && *offset != 0 {
			return fmt.Errorf("--all cannot be combined with nonzero --offset %d", *offset)
		}
		if *all {
			result, err = c.AllApplications(ctx)
		} else {
			result, err = c.Applications(ctx, *offset)
		}
	case "application submit":
		result, err = c.SubmitApplication(ctx, draft, *reviewed)
	case "application verify":
		var saved service.Object
		if err = readJSON(*receiptFile, &saved); err != nil {
			return err
		}
		receipt, ok := saved["receipt"].(map[string]any)
		if !ok {
			return fmt.Errorf("saved receipt %q has no application receipt object", *receiptFile)
		}
		result, err = c.VerifyApplication(ctx, draft, receipt)
	default:
		var id domain.VacancyID
		id, err = domain.NewVacancyIDFromValue(*idValue)
		if err != nil {
			return err
		}
		switch args[0] + " " + args[1] {
		case "vacancies get":
			result, err = c.Vacancy(ctx, id)
		case "application status":
			result, err = c.Application(ctx, id)
		case "application prepare":
			var answers service.ApplicationAnswers
			if err = readJSON(*answersFile, &answers); err != nil {
				return err
			}
			var letter []byte
			letter, err = os.ReadFile(*letterFile)
			if err == nil {
				result, err = c.PrepareApplication(ctx, id, *cv, string(letter), answers)
			}
		}
	}
	if err != nil {
		return err
	}
	return privateJSON(*output, result, out)
}

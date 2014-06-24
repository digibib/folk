package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cznic/ql"
	"github.com/rcrowley/go-tigertonic"
	log "gopkg.in/inconshreveable/log15.v2"
)

var (
	schema = ql.MustCompile(`
BEGIN TRANSACTION;

	CREATE TABLE IF NOT EXISTS Department (
		Name string,
		Parent int64
	);

	CREATE TABLE IF NOT EXISTS Person (
		Name string,
		Dept int64,
		Email string,
		Phone string,
		Img string,
		Role string,
		Info string,
		Updated time
	);

COMMIT;
`)
	qGetDept        = ql.MustCompile(`SELECT id(), Name, Parent FROM Department WHERE id() == $1`)
	qGetAllDepts    = `SELECT id(), Name, Parent FROM Department ORDER BY Name ASC`
	qInsertDept     = ql.MustCompile(`BEGIN TRANSACTION; INSERT INTO Department VALUES($1, $2); COMMIT;`)
	qDeleteDept     = ql.MustCompile(`BEGIN TRANSACTION; DELETE FROM Department WHERE id() == $1; COMMIT;`)
	qUpdateDept     = ql.MustCompile(`BEGIN TRANSACTION; UPDATE Department SET Name = $1, Parent = $2 WHERE id() == $3; COMMIT;`)
	qDeptHasPersons = ql.MustCompile(`SELECT id() FROM Person WHERE Dept == $1;`)
	qDeptHasDept    = ql.MustCompile(`SELECT id() FROM Department WHERE Parent == $1;`)
)

type department struct {
	ID     int64
	Name   string
	Parent int64
}

type person struct {
	ID     int64
	Name   string
	Dept   int64
	Email  string
	Phone  string
	Img    string
	Role   string
	Info   string
	Update time.Time
}

type deletedMsg struct {
	Type string
	ID   int64
}

// createSchema creates the database tables, if they don't allready exists.
func createSchema(db *ql.DB) error {
	ctx := ql.NewRWCtx()

	if _, _, err := db.Execute(ctx, schema); err != nil {
		return err
	}

	return nil
}

func setupAPIRouting() {
	apiMux = tigertonic.NewTrieServeMux()
	apiMux.Handle(
		"GET",
		"/department/{id}",
		tigertonic.Marshaled(getDepartment))
	apiMux.Handle(
		"GET",
		"/department",
		tigertonic.Marshaled(getAllDepartments))
	apiMux.Handle(
		"POST",
		"/department",
		tigertonic.Marshaled(createDepartment))
	apiMux.Handle(
		"DELETE",
		"/department/{id}",
		tigertonic.Marshaled(getDepartment))
	apiMux.Handle(
		"PUT",
		"/department/{id}",
		tigertonic.Marshaled(updateDepartment))
}

// GET /department/{id}
func getDepartment(u *url.URL, h http.Header, _ interface{}) (int, http.Header, *department, error) {
	idStr := u.Query().Get("id")
	if idStr == "" {
		return http.StatusBadRequest, nil, nil, errors.New("missing ID parameter")
	}
	id, err := strconv.Atoi(idStr)
	if err != nil {
		return http.StatusBadRequest, nil, nil, errors.New("department ID must be an integer")
	}

	ctx := ql.NewRWCtx()

	rs, _, err := db.Execute(ctx, qGetDept, int64(id))
	if err != nil {
		log.Error("database query failed", log.Ctx{"function": "getDepartment", "error": err.Error()})
		return http.StatusInternalServerError, nil, nil, errors.New("server error: database query failed")
	}

	row, err := rs[0].FirstRow()
	if err != nil {
		log.Error("database query failed", log.Ctx{"function": "getDepartment", "error": err.Error()})
		return http.StatusInternalServerError, nil, nil, errors.New("server error: database query failed")
	}

	if row == nil {
		return http.StatusNotFound, nil, nil, errors.New("department not found")
	}

	dept := department{}
	if err = ql.Unmarshal(&dept, row); err != nil {
		log.Error("failed to marshal db row", log.Ctx{"function": "getDepartment", "error": err.Error()})
		return http.StatusInternalServerError, nil, nil, errors.New("server error: database query failed")
	}
	return http.StatusOK, nil, &dept, nil
}

// GET /department
func getAllDepartments(u *url.URL, h http.Header, _ interface{}) (int, http.Header, []*department, error) {

	ctx := ql.NewRWCtx()
	rs, _, err := db.Run(ctx, qGetAllDepts)
	if err != nil {
		log.Error("database query failed", log.Ctx{"function": "getAllDepartments", "error": err.Error()})
		return http.StatusInternalServerError, nil, nil, errors.New("server error: database query failed")
	}

	sortedDepts := make(map[int64][]*department)
	for _, rs := range rs {
		if err := rs.Do(false, func(data []interface{}) (bool, error) {
			d := &department{}
			if err := ql.Unmarshal(d, data); err != nil {
				return false, err
			}
			sortedDepts[d.Parent] = append(sortedDepts[d.Parent], d)
			return true, nil
		}); err != nil {
			log.Error("failed to unmarshal departments", log.Ctx{"function": "getAllDepartments", "error": err.Error()})
			return http.StatusInternalServerError, nil, nil, errors.New("server error: database query failed")
		}
	}

	// Sort departments by subdepartments following their parent department
	departments := make([]*department, 0)
	for _, main := range sortedDepts[0] {
		departments = append(departments, main)
		for _, sub := range sortedDepts[main.ID] {
			departments = append(departments, sub)
		}
	}
	return http.StatusOK, nil, departments, nil
}

// POST /department
func createDepartment(u *url.URL, h http.Header, dept *department) (int, http.Header, *department, error) {
	if strings.TrimSpace(dept.Name) == "" {
		return http.StatusBadRequest, nil, nil, errors.New("department must have a name")
	}

	ctx := ql.NewRWCtx()
	if _, _, err := db.Execute(ctx, qInsertDept, ql.MustMarshal(dept)...); err != nil {
		log.Error("failed insert into table Department", log.Ctx{"function": "createtDepartment", "error": err.Error()})
		return http.StatusInternalServerError, nil, nil, errors.New("server error: database insert failed")
	}

	dept.ID = ctx.LastInsertID

	log.Info("department created", log.Ctx{"ID": dept.ID, "Name": dept.Name})
	return http.StatusCreated, http.Header{
			"Content-Location": {fmt.Sprintf(
				"%s://%s/api/department/%d",
				u.Scheme,
				u.Host,
				dept.ID,
			)},
		},
		dept, nil
}

// DELETE /department/{id}
func deleteDepartment(u *url.URL, h http.Header, _ interface{}) (int, http.Header, interface{}, error) {
	idStr := u.Query().Get("id")
	if idStr == "" {
		return http.StatusBadRequest, nil, nil, errors.New("missing ID parameter")
	}
	id, err := strconv.Atoi(idStr)
	if err != nil {
		return http.StatusBadRequest, nil, nil, errors.New("department ID must be an integer")
	}

	ctx := ql.NewRWCtx()

	// Make sure department does not have any persons associated with it.
	rs, _, err := db.Execute(ctx, qDeptHasPersons, int64(id))
	if err != nil {
		log.Error("database query failed", log.Ctx{"function": "deleteDepartment", "error": err.Error()})
		return http.StatusInternalServerError, nil, nil, errors.New("server error: database query failed")
	}

	row, err := rs[0].FirstRow()
	if err != nil {
		log.Error("database query failed", log.Ctx{"function": "deleteDepartment", "error": err.Error()})
		return http.StatusInternalServerError, nil, nil, errors.New("server error: database query failed")
	}

	if row != nil {
		return http.StatusBadRequest, nil, nil, errors.New("cannot delete department with associated staff")
	}

	// Make sure department has no subdepartments
	rs, _, err = db.Execute(ctx, qDeptHasDept, int64(id))
	if err != nil {
		log.Error("database query failed", log.Ctx{"function": "deleteDepartment", "error": err.Error()})
		return http.StatusInternalServerError, nil, nil, errors.New("server error: database query failed")
	}

	row, err = rs[0].FirstRow()
	if err != nil {
		log.Error("database query failed", log.Ctx{"function": "deleteDepartment", "error": err.Error()})
		return http.StatusInternalServerError, nil, nil, errors.New("server error: database query failed")
	}

	if row != nil {
		return http.StatusBadRequest, nil, nil, errors.New("cannot delete department with subdepartments")
	}

	// Try to delete
	rs, _, err = db.Execute(ctx, qDeleteDept, int64(id))
	if err != nil {
		log.Error("database query failed", log.Ctx{"function": "deleteDepartment", "error": err.Error()})
		return http.StatusInternalServerError, nil, nil, errors.New("server error: database query failed")
	}

	if ctx.RowsAffected == 0 {
		return http.StatusNotFound, nil, nil, errors.New("department does not exist")
	}

	return http.StatusNoContent, nil, nil, nil
}

// PUT /department/{id}
func updateDepartment(u *url.URL, h http.Header, dept *department) (int, http.Header, *department, error) {
	idStr := u.Query().Get("id")
	if idStr == "" {
		return http.StatusBadRequest, nil, nil, errors.New("missing ID parameter")
	}
	id, err := strconv.Atoi(idStr)
	if err != nil {
		return http.StatusBadRequest, nil, nil, errors.New("department ID must be an integer")
	}

	if strings.TrimSpace(dept.Name) == "" {
		return http.StatusBadRequest, nil, nil, errors.New("department must have a name")
	}

	ctx := ql.NewRWCtx()
	dept.ID = int64(id)
	if _, _, err := db.Execute(ctx, qUpdateDept, dept.Name, dept.Parent, dept.ID); err != nil {
		log.Error("database query failed", log.Ctx{"function": "updateDepartment", "error": err.Error()})
		return http.StatusInternalServerError, nil, nil, errors.New("server error: database query failed")
	}

	return http.StatusOK, nil, dept, nil
}

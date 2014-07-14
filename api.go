package main

import (
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/cznic/ql"
	"github.com/knakk/ftx/index"
	"github.com/knakk/intset"
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
	qGetPerson      = ql.MustCompile(`SELECT id(), Name, Dept, Email, Img, Role, Info, Phone, Updated FROM Person WHERE id() == $1`)
	qGetAllPersons  = ql.MustCompile(`SELECT id(), Name, Dept, Email, Img, Role, Info, Phone, Updated FROM Person ORDER BY id() DESC LIMIT $2 OFFSET $1;`)
	qInsertPerson   = ql.MustCompile(`BEGIN TRANSACTION; INSERT INTO Person VALUES($1, $2, $3, $4, $5, $6, $7, now()); COMMIT;`)
	qUpdatePerson   = ql.MustCompile(`BEGIN TRANSACTION; UPDATE Person SET Name = $1, Dept = $2, Email = $3, Img = $4, Role = $5, Info = $6, Phone = $7, Updated = now() WHERE id() == $8; COMMIT;`)
	qDeletePerson   = ql.MustCompile(`BEGIN TRANSACTION; DELETE FROM Person WHERE id() == $1; COMMIT;`)
	qImageUsed      = ql.MustCompile(`SELECT id() FROM Person WHERE Img == $1;`)
)

type department struct {
	ID     int64
	Name   string
	Parent int64
}

type person struct {
	ID      int64
	Name    string
	Dept    int64
	Email   string
	Img     string
	Role    string
	Info    string
	Phone   string
	Updated time.Time
}

type deletedMsg struct {
	Type string
	ID   int64
}

type searchResults struct {
	TookMs float64
	Count  int
	Hits   []int
}

// srAsIntSet returns a integer set out of a search result from an index.
func srAsIntSet(sr *index.SearchResults) *intset.BitSet {
	s := intset.NewBitSet(0)
	for _, h := range sr.Hits {
		s.Add(h.ID)
	}
	return s
}

// createSchema creates the database tables, if they don't allready exists.
func createSchema(db *ql.DB) error {
	ctx := ql.NewRWCtx()

	if _, _, err := db.Execute(ctx, schema); err != nil {
		return err
	}

	return nil
}

// shufflePerson reorders a slice of person in random order, using the
// Fisher-Yates algorithm.
func shufflePersons(ps []*person) {
	for i := 1; i < len(ps); i++ {
		r := rand.Intn(i + 1)
		if i != r {
			ps[r], ps[i] = ps[i], ps[r]
		}
	}
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
		tigertonic.Marshaled(deleteDepartment))
	apiMux.Handle(
		"PUT",
		"/department/{id}",
		tigertonic.Marshaled(updateDepartment))
	apiMux.Handle(
		"GET",
		"/person/{id}",
		tigertonic.Marshaled(getPerson))
	apiMux.Handle(
		"GET",
		"/person",
		tigertonic.Marshaled(getAllPersons))
	apiMux.Handle(
		"POST",
		"/person",
		tigertonic.Marshaled(createPerson))
	apiMux.Handle(
		"PUT",
		"/person/{id}",
		tigertonic.Marshaled(updatePerson))
	apiMux.Handle(
		"DELETE",
		"/person/{id}",
		tigertonic.Marshaled(deletePerson))
	apiMux.Handle(
		"GET",
		"/images",
		tigertonic.Marshaled(getImages))
	apiMux.Handle(
		"DELETE",
		"/image/{filename}",
		tigertonic.Marshaled(deleteImage))
	apiMux.Handle(
		"GET",
		"/search",
		tigertonic.Marshaled(searchPersons))
}

// GET /images
func getImages(u *url.URL, h http.Header, _ interface{}) (int, http.Header, []string, error) {
	imageFiles.RLock()
	defer imageFiles.RUnlock()
	return http.StatusOK, nil, imageFiles.list, nil
}

// DELETE /image/{filename}
func deleteImage(u *url.URL, h http.Header, _ interface{}) (int, http.Header, interface{}, error) {
	filename := u.Query().Get("filename")
	if filename == "" {
		return http.StatusBadRequest, nil, nil, errors.New("missing filename parameter")
	}

	// Make sure the image file is not associated with any person.
	ctx := ql.NewRWCtx()

	rs, _, err := db.Execute(ctx, qImageUsed, filename)
	if err != nil {
		log.Error("database query failed", log.Ctx{"function": "deleteImage", "error": err.Error()})
		return http.StatusInternalServerError, nil, nil, errors.New("server error: database query failed")
	}

	row, err := rs[0].FirstRow()
	if err != nil {
		log.Error("database query failed", log.Ctx{"function": "deleteImage", "error": err.Error()})
		return http.StatusInternalServerError, nil, nil, errors.New("server error: database query failed")
	}

	if row != nil {
		return http.StatusBadRequest, nil, nil, errors.New("image is in use; cannot delete")
	}

	err = os.Remove(fmt.Sprintf("data/public/img/%s", filename))
	if err != nil {
		log.Error("failed to delete file", log.Ctx{"error": err.Error()})
		return http.StatusInternalServerError, nil, nil, errors.New("server error: failed to delete file")
	}

	imageFiles.Lock()
	for i, f := range imageFiles.list {
		if f == filename {
			imageFiles.list = append(imageFiles.list[:i], imageFiles.list[i+1:]...)
			break
		}
	}
	imageFiles.Unlock()

	log.Info("image deleted", log.Ctx{"filename": filename})

	return http.StatusNoContent, nil, nil, nil
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
		log.Error("failed insert into table Department", log.Ctx{"function": "createDepartment", "error": err.Error()})
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

	log.Info("department deleted", log.Ctx{"ID": id})

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

	log.Info("department updated", log.Ctx{"ID": id, "Name": dept.Name})

	return http.StatusOK, nil, dept, nil
}

// GET /person/{id}
func getPerson(u *url.URL, h http.Header, _ interface{}) (int, http.Header, *person, error) {
	idStr := u.Query().Get("id")
	if idStr == "" {
		return http.StatusBadRequest, nil, nil, errors.New("missing ID parameter")
	}
	id, err := strconv.Atoi(idStr)
	if err != nil {
		return http.StatusBadRequest, nil, nil, errors.New("person ID must be an integer")
	}

	ctx := ql.NewRWCtx()

	rs, _, err := db.Execute(ctx, qGetPerson, int64(id))
	if err != nil {
		log.Error("database query failed", log.Ctx{"function": "getPerson", "error": err.Error()})
		return http.StatusInternalServerError, nil, nil, errors.New("server error: database query failed")
	}

	row, err := rs[0].FirstRow()
	if err != nil {
		log.Error("database query failed", log.Ctx{"function": "getPerson", "error": err.Error()})
		return http.StatusInternalServerError, nil, nil, errors.New("server error: database query failed")
	}

	if row == nil {
		return http.StatusNotFound, nil, nil, errors.New("person not found")
	}

	p := person{}
	if err = ql.Unmarshal(&p, row); err != nil {
		log.Error("failed to marshal db row", log.Ctx{"function": "getPerson", "error": err.Error()})
		return http.StatusInternalServerError, nil, nil, errors.New("server error: database query failed")
	}
	return http.StatusOK, nil, &p, nil
}

// POST /person
func createPerson(u *url.URL, h http.Header, p *person) (int, http.Header, *person, error) {
	if strings.TrimSpace(p.Name) == "" {
		return http.StatusBadRequest, nil, nil, errors.New("person must have a name")
	}

	if p.Dept == 0 {
		return http.StatusBadRequest, nil, nil, errors.New("person must belong to a department")
	}

	ctx := ql.NewRWCtx()

	rs, _, err := db.Execute(ctx, qGetDept, p.Dept)
	if err != nil {
		log.Error("database query failed", log.Ctx{"function": "getPerson", "error": err.Error()})
		return http.StatusInternalServerError, nil, nil, errors.New("server error: database query failed")
	}

	row, err := rs[0].FirstRow()
	if err != nil {
		log.Error("database query failed", log.Ctx{"function": "getPerson", "error": err.Error()})
		return http.StatusInternalServerError, nil, nil, errors.New("server error: database query failed")
	}

	if row == nil {
		return http.StatusNotFound, nil, nil, errors.New("department does not exist")
	}

	if _, _, err := db.Execute(ctx, qInsertPerson, ql.MustMarshal(p)...); err != nil {
		log.Error("failed insert into table Person", log.Ctx{"function": "createPerson", "error": err.Error()})
		return http.StatusInternalServerError, nil, nil, errors.New("server error: database insert failed")
	}

	p.ID = ctx.LastInsertID

	go func() {
		analyzer.Index(fmt.Sprintf("%v %v %v", p.Name, p.Role, p.Info), int(p.ID))
	}()

	log.Info("person created", log.Ctx{"ID": p.ID, "Name": p.Name, "Dept": p.Dept, "Email": p.Email, "Image": p.Img})
	return http.StatusCreated, http.Header{
			"Content-Location": {fmt.Sprintf(
				"%s://%s/api/person/%d",
				u.Scheme,
				u.Host,
				p.ID,
			)},
		},
		p, nil
}

// PUT /person/{id}
func updatePerson(u *url.URL, h http.Header, p *person) (int, http.Header, *person, error) {
	idStr := u.Query().Get("id")
	if idStr == "" {
		return http.StatusBadRequest, nil, nil, errors.New("missing ID parameter")
	}
	id, err := strconv.Atoi(idStr)
	if err != nil {
		return http.StatusBadRequest, nil, nil, errors.New("person ID must be an integer")
	}

	if p.Dept == 0 {
		return http.StatusBadRequest, nil, nil, errors.New("person must belong to a department")
	}

	if strings.TrimSpace(p.Name) == "" {
		return http.StatusBadRequest, nil, nil, errors.New("person must have a name")
	}

	ctx := ql.NewRWCtx()

	// get old person, so we can unindex
	rs, _, err := db.Execute(ctx, qGetPerson, int64(id))
	if err != nil {
		log.Error("database query failed", log.Ctx{"function": "getPerson", "error": err.Error()})
		return http.StatusInternalServerError, nil, nil, errors.New("server error: database query failed")
	}

	row, err := rs[0].FirstRow()
	if err != nil {
		log.Error("database query failed", log.Ctx{"function": "getPerson", "error": err.Error()})
		return http.StatusInternalServerError, nil, nil, errors.New("server error: database query failed")
	}

	if row == nil {
		return http.StatusNotFound, nil, nil, errors.New("person not found")
	}

	oldp := person{}
	if err = ql.Unmarshal(&oldp, row); err != nil {
		log.Error("failed to marshal db row", log.Ctx{"function": "getPerson", "error": err.Error()})
		return http.StatusInternalServerError, nil, nil, errors.New("server error: database query failed")
	}

	// check for existing department
	rs, _, err = db.Execute(ctx, qGetDept, p.Dept)
	if err != nil {
		log.Error("database query failed", log.Ctx{"function": "updatePerson", "error": err.Error()})
		return http.StatusInternalServerError, nil, nil, errors.New("server error: database query failed")
	}

	row, err = rs[0].FirstRow()
	if err != nil {
		log.Error("database query failed", log.Ctx{"function": "updatePerson", "error": err.Error()})
		return http.StatusInternalServerError, nil, nil, errors.New("server error: database query failed")
	}

	if row == nil {
		return http.StatusNotFound, nil, nil, errors.New("department does not exist")
	}

	// update
	if _, _, err := db.Execute(ctx, qUpdatePerson, p.Name, p.Dept, p.Email, p.Img, p.Role, p.Info, p.Phone, int64(id)); err != nil {
		log.Error("database query failed", log.Ctx{"function": "updateDepartment", "error": err.Error()})
		return http.StatusInternalServerError, nil, nil, errors.New("server error: database query failed")
	}

	go func() {
		analyzer.UnIndex(fmt.Sprintf("%v %v %v", oldp.Name, oldp.Role, oldp.Info), id)
		analyzer.Index(fmt.Sprintf("%v %v %v", p.Name, p.Role, p.Info), id)
	}()

	log.Info("person updated",
		log.Ctx{"ID": p.ID, "Name": p.Name, "Dept": p.Dept, "Email": p.Email, "Image": p.Img, "Info": p.Info, "Role": p.Role, "Phone": p.Phone})
	p.Updated = time.Now()
	return http.StatusOK, nil, p, nil
}

// DELETE /person/{id}
func deletePerson(u *url.URL, h http.Header, _ interface{}) (int, http.Header, interface{}, error) {
	idStr := u.Query().Get("id")
	if idStr == "" {
		return http.StatusBadRequest, nil, nil, errors.New("missing ID parameter")
	}
	id, err := strconv.Atoi(idStr)
	if err != nil {
		return http.StatusBadRequest, nil, nil, errors.New("person ID must be an integer")
	}

	ctx := ql.NewRWCtx()

	// get old person, so we can unindex
	rs, _, err := db.Execute(ctx, qGetPerson, int64(id))
	if err != nil {
		log.Error("database query failed", log.Ctx{"function": "getPerson", "error": err.Error()})
		return http.StatusInternalServerError, nil, nil, errors.New("server error: database query failed")
	}

	row, err := rs[0].FirstRow()
	if err != nil {
		log.Error("database query failed", log.Ctx{"function": "getPerson", "error": err.Error()})
		return http.StatusInternalServerError, nil, nil, errors.New("server error: database query failed")
	}

	if row == nil {
		return http.StatusNotFound, nil, nil, errors.New("person not found")
	}

	oldp := person{}
	if err = ql.Unmarshal(&oldp, row); err != nil {
		log.Error("failed to marshal db row", log.Ctx{"function": "getPerson", "error": err.Error()})
		return http.StatusInternalServerError, nil, nil, errors.New("server error: database query failed")
	}

	_, _, err = db.Execute(ctx, qDeletePerson, int64(id))
	if err != nil {
		log.Error("database query failed", log.Ctx{"function": "deletePerson", "error": err.Error()})
		return http.StatusInternalServerError, nil, nil, errors.New("server error: database query failed")
	}

	if ctx.RowsAffected == 0 {
		return http.StatusNotFound, nil, nil, errors.New("person does not exist")
	}

	log.Info("person deleted", log.Ctx{"ID": id})

	go func() {
		analyzer.UnIndex(fmt.Sprintf("%v %v %v", oldp.Name, oldp.Role, oldp.Info), id)
	}()

	return http.StatusNoContent, nil, nil, nil
}

// GET /person
func getAllPersons(u *url.URL, h http.Header, _ interface{}) (int, http.Header, []*person, error) {
	var offset, limit int
	var err error
	offsetStr := u.Query().Get("offset")
	if offsetStr != "" {
		offset, err = strconv.Atoi(offsetStr)
		if err != nil {
			return http.StatusBadRequest, nil, nil, errors.New("offset parameter must be an integer")
		}
	}

	limitStr := u.Query().Get("limit")
	if limitStr == "" {
		limit = MaxPersonsLimit
	} else {
		limit, err = strconv.Atoi(limitStr)
		if err != nil {
			return http.StatusBadRequest, nil, nil, errors.New("limit parameter must be an integer")
		}
	}

	ctx := ql.NewRWCtx()
	rs, _, err := db.Execute(ctx, qGetAllPersons, int64(offset), int64(limit))
	if err != nil {
		log.Error("database query failed", log.Ctx{"function": "getAllPersons", "error": err.Error()})
		return http.StatusInternalServerError, nil, nil, errors.New("server error: database query failed")
	}

	var persons []*person
	for _, rs := range rs {
		if err := rs.Do(false, func(data []interface{}) (bool, error) {
			p := &person{}
			if err := ql.Unmarshal(p, data); err != nil {
				return false, err
			}
			persons = append(persons, p)
			return true, nil
		}); err != nil {
			log.Error("failed to unmarshal persons", log.Ctx{"function": "getAllPersons", "error": err.Error()})
			return http.StatusInternalServerError, nil, nil, errors.New("server error: database query failed")
		}
	}

	order := u.Query().Get("order")
	if order == "random" {
		shufflePersons(persons)
	}

	return http.StatusOK, nil, persons, nil
}

// GET /search
func searchPersons(u *url.URL, h http.Header, _ interface{}) (int, http.Header, *searchResults, error) {

	res := &searchResults{}
	t0 := time.Now()
	q := u.Query().Get("q")
	parsedQuery := strings.Split(strings.ToLower(q), " ")

	query := index.NewQuery().Must(parsedQuery)
	hits := analyzer.Idx.Query(query)
	hitsSet := srAsIntSet(hits)
	res.Count = hitsSet.Size()
	res.Hits = hitsSet.All()
	res.TookMs = float64(time.Now().Sub(t0)) / 1000000

	return http.StatusOK, nil, res, nil
}

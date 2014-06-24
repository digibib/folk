package main

import (
	"fmt"
	"net/http"
	"os"
	"reflect"
	"testing"

	"github.com/cznic/ql"
	"github.com/rcrowley/go-tigertonic"
	"github.com/rcrowley/go-tigertonic/mocking"
)

var testMux = tigertonic.NewHostServeMux()

func init() {
	var err error
	db, err = ql.OpenMem()
	if err != nil {
		println(err.Error())
		os.Exit(1)
	}

	err = createSchema(db)
	if err != nil {
		println(err.Error())
		os.Exit(1)
	}
	inserts := ql.MustCompile(`
	BEGIN TRANSACTION;
		INSERT INTO Department VALUES ("mainB", 0), ("mainA", 0), ("mainC", 0);
		INSERT INTO Department VALUES ("subA1", 2), ("subA2", 2), ("subB1", 1);
		INSERT INTO Person VALUES ("Mr. A", 4, "a@com", "", "a.png", "", "", now());
	COMMIT;
	`)

	ctx := ql.NewRWCtx()
	if _, _, err := db.Execute(ctx, inserts); err != nil {
		println(err.Error())
		os.Exit(1)
	}

	setupAPIRouting()
	nsMux := tigertonic.NewTrieServeMux()
	nsMux.HandleNamespace("/api", apiMux)
	testMux.Handle("test.com", nsMux)
}

func TestGetDepartment(t *testing.T) {
	status, _, response, err := getDepartment(
		mocking.URL(testMux, "GET", "http://test.com/api/department/1"),
		mocking.Header(nil),
		nil,
	)

	if err != nil {
		t.Errorf("getDepartment existing dept should succed, got error: %v", err)
	}

	if status != http.StatusOK {
		t.Errorf("want => %v, got %v", http.StatusOK, status)
	}

	want := &department{ID: 1, Name: "mainB"}
	if !reflect.DeepEqual(want, response) {
		t.Errorf("want => %v, got %v", want, response)
	}

	status, _, response, err = getDepartment(
		mocking.URL(testMux, "GET", "http://test.com/api/department/10"),
		mocking.Header(nil),
		nil,
	)

	if err == nil || err.Error() != "department not found" {
		t.Error("getDepartment non-existing dept should return an error")
	}

	if status != http.StatusNotFound {
		t.Errorf("want %v, got %v", http.StatusNotFound, status)
	}
}

func TestGetAllDepartments(t *testing.T) {
	status, _, response, err := getAllDepartments(
		mocking.URL(testMux, "GET", "http://test.com/api/department"),
		mocking.Header(nil),
		nil,
	)

	if err != nil {
		t.Errorf("getAllDepartments should always succed, got error: %v", err.Error())
	}

	if status != http.StatusOK {
		t.Errorf("want => %v, got %v", http.StatusOK, status)
	}

	if len(response) != 6 {
		t.Errorf("want => 6, got %v", len(response))
	}

	orderWant := []string{"mainA", "subA1", "subA2", "mainB", "subB1", "mainC"}
	orderGot := []string{}
	for _, d := range response {
		orderGot = append(orderGot, d.Name)
	}

	if !reflect.DeepEqual(orderWant, orderGot) {
		t.Errorf("getAllDeparmtents order: want %v, got %v", orderWant, orderGot)
	}
}

func TestCreateDepartment(t *testing.T) {
	status, _, _, err := createDepartment(
		mocking.URL(testMux, "POST", "http://test.com/api/department"),
		mocking.Header(nil),
		&department{},
	)

	if err == nil || err.Error() != "department must have a name" {
		t.Error("creating department with empty name should fail")
	}

	status, header, response, err := createDepartment(
		mocking.URL(testMux, "POST", "http://test.com/api/department"),
		mocking.Header(nil),
		&department{Name: "NewD"},
	)

	if err != nil {
		t.Error("createDepartment should succeed, got error: %v", err)
	}

	if status != http.StatusCreated {
		t.Errorf("want => %v, got %v", http.StatusCreated, status)
	}

	if response.Name != "NewD" {
		t.Errorf("unexpected response: %v", response)
	}

	if header.Get("Content-Location") != fmt.Sprintf("http://test.com/api/department/%v", response.ID) {
		t.Errorf("header doesn't contain correct content-location: %v", header)
	}
}

func TestDeleteDepartment(t *testing.T) {
	status, _, _, err := deleteDepartment(
		mocking.URL(testMux, "DELETE", "http://test.com/api/department/4"),
		mocking.Header(nil),
		nil,
	)

	if err == nil || err.Error() != "cannot delete department with associated staff" {
		t.Error("deleteDepartment should not succeed if department has persons belonging to it")
	}

	if status != http.StatusBadRequest {
		t.Errorf("want => %v, got %v", http.StatusBadRequest, status)
	}

	status, _, _, err = deleteDepartment(
		mocking.URL(testMux, "DELETE", "http://test.com/api/department/1"),
		mocking.Header(nil),
		nil,
	)

	if err == nil || err.Error() != "cannot delete department with subdepartments" {
		t.Error("deleteDepartment should not succeed if department has subdepartments")
	}

	if status != http.StatusBadRequest {
		t.Errorf("want => %v, got %v", http.StatusBadRequest, status)
	}

	status, _, _, err = deleteDepartment(
		mocking.URL(testMux, "DELETE", "http://test.com/api/department/99"),
		mocking.Header(nil),
		nil,
	)

	if err == nil || err.Error() != "department does not exist" {
		t.Error("deleteDepartment should not succeed if department does not exist")
	}

	if status != http.StatusNotFound {
		t.Errorf("want => %v, got %v", http.StatusNotFound, status)
	}

	status, _, _, err = deleteDepartment(
		mocking.URL(testMux, "DELETE", "http://test.com/api/department/3"),
		mocking.Header(nil),
		nil,
	)

	if err != nil {
		t.Error("deleteDepartment should succeed if department exist and have no persons or subdepartments")
	}

	if status != http.StatusNoContent {
		t.Errorf("want => %v, got %v", http.StatusNoContent, status)
	}
}

func TestUpdateDepartment(t *testing.T) {
	status, _, response, err := updateDepartment(
		mocking.URL(testMux, "PUT", "http://test.com/api/department/1"),
		mocking.Header(nil),
		&department{Name: "mainA+"},
	)

	if err != nil {
		t.Errorf("updateDepartment should succeed, got errror: %v", err.Error())
	}

	if status != http.StatusOK {
		t.Errorf("want => %v, got %v", http.StatusOK, status)
	}

	if response.Name != "mainA+" || response.ID != 1 {
		t.Errorf("updateDepartment should return update response, got %+v", response)
	}
}

package storage

import (
	"fmt"
	"log"
	"strings"

	"github.com/jinzhu/gorm"
	// This import is needed in order to utilize MySql
	_ "github.com/jinzhu/gorm/dialects/mysql"
	"github.com/klyngen/flightlogger/common"
	"github.com/pkg/errors"
)

// OrmDatabase - should implement the databaseInterface
type OrmDatabase struct {
	db *gorm.DB
}

// MigrateDatabase - migrates the database
func (d *OrmDatabase) MigrateDatabase() error {
	// Migrate location first

	err := d.db.AutoMigrate(&DbCountryPart{}).Error
	err = d.db.AutoMigrate(&DbFileReference{}).Error
	err = d.db.AutoMigrate(&DbCoordinates{}).Error
	err = d.db.AutoMigrate(&DbLocation{}).Error

	if err != nil {
		return errors.Wrap(err, "Unable to migrate basic Location-coordinates")
	}

	// Create club entity before user and flights
	err = d.db.AutoMigrate(&DbClub{}).Error

	// Waypoint and start are dependent on location
	err = d.db.AutoMigrate(&DbWaypoint{}).Error
	err = d.db.AutoMigrate(&DbStartSite{}).Error

	if err != nil {
		return errors.Wrap(err, "Unable to migrate flight base-entities")
	}

	// Wing related data
	err = d.db.AutoMigrate(&DbWingScoreDetails{}).Error
	err = d.db.AutoMigrate(&DbWing{}).Error

	if err != nil {
		return errors.Wrap(err, "Unable to migrate wing-entities")
	}

	// Flight related entities
	err = d.db.AutoMigrate(&DbFlightType{}).Error
	err = d.db.AutoMigrate(&DbTakeoffType{}).Error
	err = d.db.AutoMigrate(&DbIncident{}).Error
	err = d.db.AutoMigrate(&DbFlight{}).Error

	if err != nil {
		return errors.Wrap(err, "Unable to migrate flight-entities")
	}

	// Set up the user related entities
	err = d.db.AutoMigrate(&DbCredentials{}).Error
	err = d.db.AutoMigrate(&DbUserScope{}).Error
	err = d.db.AutoMigrate(&DbUserGroup{}).Error
	err = d.db.AutoMigrate(&DbUser{}).Error
	if err != nil {
		return errors.Wrap(err, "Unable to migrate user-entities")
	}

	err = d.db.Model(&DbCredentials{}).AddForeignKey("user_id", "db_users(id)", "CASCADE", "CASCADE").Error
	err = d.db.Model(&DbLocation{}).AddForeignKey("countrypart_referer", "db_countryparts(id)", "SET NULL", "SET NULL").Error
	err = d.db.Model(&DbLocation{}).AddForeignKey("coordinates_referer", "db_coordinates(id)", "SET NULL", "SET NULL").Error

	if err != nil {
		return errors.Wrap(err, "Unable to establich foreign keys")
	}
	return errors.Wrap(err, "Unable to migrate the database")
}

// CreateConnection - establish a connection to the database
func (d *OrmDatabase) CreateConnection(username string, password string, database string, port string, hostname string) error {
	db, err := gorm.Open("mysql", fmt.Sprintf("%s:%s@/%s?charset=utf8&parseTime=True&loc=Local", username, password, database))

	if err != nil {
		return err
	}

	d.db = db
	return nil
}

// ############## USER RELATED QUERIES ############################

// CreateUser - try to create a new user
func (d *OrmDatabase) CreateUser(user common.User) (common.User, error) {
	mappedUser, mappedCreds := mapUser(user)
	err := d.db.Create(&mappedUser).Error

	// Create the base user entity
	if err != nil {
		return user, err
	}

	// Set the user ID of the Credentials
	mappedCreds.UserID = mappedUser.ID

	err = d.db.Create(&mappedCreds).Error

	if err != nil {

	}

	return demapUser(mappedUser), nil
}

// GetAllUsers - gets all users
func (d *OrmDatabase) GetAllUsers(limit int, page int) ([]common.User, error) {
	var users []DbUser
	d.db.Limit(limit).Offset((page - 1) * limit).Find(&users)
	return demapUsers(users), nil
}

// GetUser - gets a single user if it exists
func (d *OrmDatabase) GetUser(ID uint) (common.User, error) {
	var user DbUser
	err := errors.Wrap(d.db.First(&user, ID).Error, "Unable to get user")

	user.ID = ID

	return demapUser(user), err
}

// UpdateUser - update an existing user if it exists
func (d *OrmDatabase) UpdateUser(ID uint, user common.User) (common.User, error) {

	dbUser, _ := mapUser(user)
	dbUser.ID = ID

	// If the user has set its salt and hash, we probably want to update the credentials
	if user.PasswordSalt != nil && user.PasswordHash != nil {
		var creds DbCredentials
		err := d.db.Where("user_id = ?", ID).First(&creds).Error

		if err != nil {
			return user, errors.Wrap(err, "Unable to update password details")
		}

		// Set the password
		creds.PasswordHash = user.PasswordHash
		creds.PasswordSalt = user.PasswordSalt

		err = d.db.Save(&creds).Error

		if err != nil {
			return user, errors.Wrap(err, "Unable to update password details")
		}
	}

	return demapUser(dbUser), errors.Wrap(d.db.Save(&dbUser).Error, "Unable to update a user")
}

// DeleteUser - deletes a user
// this deletion uses a hard deletes and removes all data related to a user
func (d *OrmDatabase) DeleteUser(ID uint) error {
	var user DbUser

	err := d.db.First(&user, ID).Error

	if err != nil {
		return errors.Wrap(err, "Cannot delete a user we cannot find")
	}

	err = d.db.Model(&user).Association("Wings").Clear().Error

	if err != nil {
		errors.Wrap(err, "Unable to remove associated wings")
	}

	err = d.db.Model(&user).Association("Groups").Error

	if err != nil {
		errors.Wrap(err, "Unable to remove associated groups")
	}

	err = d.db.Model(&user).Association("Scopes").Error

	if err != nil {
		errors.Wrap(err, "Unable to remove associated scopes")
	}

	// Hard delete the user
	err = d.db.Unscoped().Delete(&user, ID).Error

	if err != nil {
		return errors.Wrap(err, "Unable to delete the user")
	}

	return nil
}

// CreateLocation - creates a location. Locations are then again used
// by StartSite, Waypoint etc
func (d *OrmDatabase) CreateLocation(location common.Location) (common.Location, error) {
	mappedLocation := mapLocation(location)

	// Store the coordinates first
	err := d.db.Create(&mappedLocation.Coordinates).Error

	if err != nil {
		return location, errors.Wrap(err, "Unable to store coordinates")
	}

	partID := d.resolveCountryPart(mappedLocation.CountryPart)

	// Make it possible to resolve the foreign key later
	mappedLocation.CoordinatesReferer = mappedLocation.Coordinates.ID
	mappedLocation.CountrypartReferer = partID
	// Then store the countrypart, if it is not empty

	err = d.db.Create(&mappedLocation).Error

	if err != nil {
		return location, errors.Wrap(err, "Could not create the location")
	}

	return demapLocation(mappedLocation), nil
}

// creates a countrypart if needed, or it will return an existing to prevent duplicates
func (d *OrmDatabase) resolveCountryPart(part DbCountryPart) uint {
	// If the part is valid
	if !part.isEmpty() {

		// See if we have such a part already
		comboID := d.getCountryPart(part)

		// If not create one
		if comboID == 0 {
			err := d.db.Create(&part).Error

			if err != nil {
				return 0
			}

			return part.ID
		}
		// return the part we got
		return comboID
	}
	return 0
}

// The reason for this not being used
func (d *OrmDatabase) getCountryPart(part DbCountryPart) uint {
	var dbPart DbCountryPart
	err := d.db.Where("area_name = ? AND postal_code = ? AND country_part = ?", part.AreaName, part.PostalCode, part.CountryPart).First(&dbPart).Error

	if err != nil {
		return 0
	}

	return dbPart.ID
}

// UpdateLocation updates the location and if needed its CountryPart and coordinates
func (d *OrmDatabase) UpdateLocation(ID uint, location common.Location) (common.Location, error) {
	var existingLocation DbLocation

	d.db.First(&existingLocation, ID)

	newCountryPart := DbCountryPart{
		AreaName:    location.AreaName,
		PostalCode:  location.PostalCode,
		CountryPart: location.CountryPart,
	}

	// resolve the country part
	partID := d.resolveCountryPart(newCountryPart)

	var coordinates DbCoordinates

	// set the coordinates for the location
	err := d.db.Model(&existingLocation).Related(&coordinates, "Coordinates").Error

	if err != nil { // The coordinates could not be found
		log.Printf("Unable to find the coordinates: %v", err)
		return location, err
	} else {
		coordinates.Longitude = existingLocation.Coordinates.Longitude
		coordinates.Lattitude = existingLocation.Coordinates.Lattitude
	}

	// A countrypart can change. The coordinates object will never be replaced once it exists
	existingLocation.CountrypartReferer = partID

	return demapLocation(existingLocation), errors.Wrap(d.db.Save(&existingLocation).Error, "Unable to update a user")
}

// DeleteLocation - softDeletes a location
func (d *OrmDatabase) DeleteLocation(ID uint) error {

	var loc DbLocation

	err := errors.Wrap(d.db.First(&loc, ID).Error, "Unable to get location")

	log.Println(loc)

	loc.ID = ID

	if err != nil {
		return errors.Wrap(err, "Cannot delete a user we cannot find")
	}

	return d.db.Delete(&loc).Error
}

// LocationSearchByName finds relevant locations based on user input
func (d *OrmDatabase) LocationSearchByName(name string) ([]common.Location, error) {
	var locations []DbLocation

	// Find results by name
	err := d.db.Where("name Like ?", strings.ToLower(name)+"%").Find(&locations).Error

	if err != nil {
		return nil, errors.Wrap(err, "Unable to find locations")
	}

	return demapLocations(locations), nil
}

// GetLocation - should get a location and its sub-entities
func (d *OrmDatabase) GetLocation(ID uint) (common.Location, error) {
	var loc DbLocation

	err := errors.Wrap(d.db.First(&loc, ID).Error, "Unable to get location")

	if err != nil {
		return demapLocation(loc), err
	}

	return demapLocation(loc), nil
}

// CreateWayPoint creates a waypoint
func (d *OrmDatabase) CreateWayPoint(point common.Waypoint) (common.Waypoint, error) {
	panic("not implemented")
}

// UpdateWayPoint soft-deletes the waypoint
func (d *OrmDatabase) UpdateWayPoint(ID uint, point common.Waypoint) (common.Waypoint, error) {
	panic("not implemented")
}

// DeleteWaypoint
func (d *OrmDatabase) DeleteWayPoint(ID uint) error {
	panic("not implemented")
}

// UserGroup CRUD and search
func (d *OrmDatabase) CreateUserGroup(userGroup common.UserGroup, scopes []int) (common.UserGroup, error) {
	panic("not implemented")
}

func (d *OrmDatabase) UpdateUserGroup(groupID int, userGroup common.UserGroup, scopes []int) (common.UserGroup, error) {
	panic("not implemented")
}

func (d *OrmDatabase) GetAllUserGroups(limit int, page int) ([]common.UserGroup, error) {
	panic("not implemented")
}

func (d *OrmDatabase) GetUserGroup(ID int) (common.UserGroup, error) {
	panic("not implemented")
}

func (d *OrmDatabase) UserGroupSearchByName(name string) ([]common.UserGroup, error) {
	panic("not implemented")
}

// FileCreation CRD
func (d *OrmDatabase) CreateFile(file common.FileReference) (common.FileReference, error) {
	panic("not implemented")
}

func (d *OrmDatabase) GetFile(ID int) (common.FileReference, error) {
	panic("not implemented")
}

func (d *OrmDatabase) DeleteFile(ID int) error {
	panic("not implemented")
}

// Flight CRUD
func (d *OrmDatabase) CreateFlight(flight common.Flight) (common.Flight, error) {
	panic("not implemented")
}

func (d *OrmDatabase) UpdateFlight(ID int, flight common.Flight) (common.Flight, error) {
	panic("not implemented")
}

func (d *OrmDatabase) DeleteFlight(ID int) error {
	panic("not implemented")

}

func (d *OrmDatabase) GetAllFlights(limit int, page int) ([]common.Flight, error) {
	panic("not implemented")
}

func (d *OrmDatabase) GetFlight(ID int) (common.Flight, error) {
	panic("not implemented")
}

// FlightIncident CRUD and search
func (d *OrmDatabase) CreateFlightIncident(incident common.Incident) (common.Incident, error) {
	panic("not implemented")
}

func (d *OrmDatabase) UpdateFlightIncident(ID int, Incident common.Incident) (common.Incident, error) {
	panic("not implemented")
}

func (d *OrmDatabase) DeleteFlightIncident(ID int) error {
	panic("not implemented")
}

func (d *OrmDatabase) GetFlightIncident(ID int) (common.Flight, error) {
	panic("not implemented")
}

func (d *OrmDatabase) GetFlightIncidentByLevel(errorLevel int) ([]common.Flight, error) {
	panic("not implemented")
}

func (d *OrmDatabase) GetFlightIncidents(limit int, page int) ([]common.Flight, error) {
	panic("not implemented")
}

// Wing CRUD
func (d *OrmDatabase) CreateWing(wing common.Wing) (common.Wing, error) {
	panic("not implemented")
}

func (d *OrmDatabase) UpdateWing(ID int, wing common.Wing) (common.Wing, error) {
	panic("not implemented")
}

func (d *OrmDatabase) DeleteWing(ID int) error {
	panic("not implemented")
}

func (d *OrmDatabase) GetWing(ID int) (common.Wing, error) {
	panic("not implemented")
}

func (d *OrmDatabase) GetAllWings(limit int, page int) (common.Wing, error) {
	panic("not implemented")
}

func (d *OrmDatabase) GetWingSearchByName(name string) ([]common.Wing, error) {
	panic("not implemented")
}

// StartSite
func (d *OrmDatabase) CreateStartSite(site common.StartSite) (common.StartSite, error) {
	panic("not implemented")
}

func (d *OrmDatabase) UpdateStartSite(ID uint, site common.StartSite) (common.StartSite, error) {
	panic("not implemented")
}

func (d *OrmDatabase) DeleteStartSite(ID uint) error {
	panic("not implemented")
}

func (d *OrmDatabase) GetStartStartSiteByName(name string) ([]common.StartSite, error) {
	panic("not implemented")
}

func (d *OrmDatabase) GetStartSiteByDifficulty(level int) ([]common.StartSite, error) {
	panic("not implemented")
}

func (d *OrmDatabase) GetStartSite(ID int) (common.StartSite, error) {
	panic("not implemented")
}

func (d *OrmDatabase) GetAllStartSites(limit int, page int) ([]common.StartSite, error) {
	panic("not implemented")
}

func (d *OrmDatabase) GetSiteIncidents(siteID uint) ([]common.Incident, error) {
	panic("not implemented")
}
